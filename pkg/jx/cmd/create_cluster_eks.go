package cmd

import (
	"github.com/jenkins-x/jx/pkg/cloud/amazon"
	"github.com/jenkins-x/jx/pkg/kube"
	"github.com/jenkins-x/jx/pkg/log"
	"github.com/jenkins-x/jx/pkg/util"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jenkins-x/jx/pkg/jx/cmd/templates"
	logger "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"gopkg.in/AlecAivazis/survey.v1/terminal"
)

// CreateClusterEKSOptions contains the CLI flags
type CreateClusterEKSOptions struct {
	CreateClusterOptions

	Flags CreateClusterEKSFlags
}

type CreateClusterEKSFlags struct {
	ClusterName         string
	NodeType            string
	NodeCount           int
	NodesMin            int
	NodesMax            int
	Region              string
	Zones               string
	Profile             string
	SshPublicKey        string
	Verbose             int
	AWSOperationTimeout time.Duration
	Tags                string
}

var (
	createClusterEKSLong = templates.LongDesc(`
		This command creates a new Kubernetes cluster on Amazon Web Services (AWS) using EKS, installing required local dependencies and provisions the
		Jenkins X platform

		EKS is a managed Kubernetes service on AWS.

`)

	createClusterEKSExample = templates.Examples(`
        # to create a new Kubernetes cluster with Jenkins X in your default zones (from $EKS_AVAILABILITY_ZONES)
		jx create cluster eks

		# to specify the zones
		jx create cluster eks --zones us-west-2a,us-west-2b,us-west-2c
`)
)

// NewCmdCreateClusterEKS creates the command
func NewCmdCreateClusterEKS(f Factory, in terminal.FileReader, out terminal.FileWriter, errOut io.Writer) *cobra.Command {
	options := CreateClusterEKSOptions{
		CreateClusterOptions: createCreateClusterOptions(f, in, out, errOut, AKS),
	}
	cmd := &cobra.Command{
		Use:     "eks",
		Short:   "Create a new Kubernetes cluster on AWS using EKS",
		Long:    createClusterEKSLong,
		Example: createClusterEKSExample,
		Run: func(cmd *cobra.Command, args []string) {
			options.Cmd = cmd
			options.Args = args
			err := options.Run()
			CheckErr(err)
		},
	}

	options.addCreateClusterFlags(cmd)
	options.addCommonFlags(cmd)

	cmd.Flags().StringVarP(&options.Flags.ClusterName, optionClusterName, "n", "", "The name of this cluster.")
	cmd.Flags().StringVarP(&options.Flags.NodeType, "node-type", "", "m5.large", "node instance type")
	cmd.Flags().IntVarP(&options.Flags.NodeCount, optionNodes, "o", -1, "number of nodes")
	cmd.Flags().IntVarP(&options.Flags.NodesMin, "nodes-min", "", -1, "minimum number of nodes")
	cmd.Flags().IntVarP(&options.Flags.NodesMax, "nodes-max", "", -1, "maximum number of nodes")
	cmd.Flags().IntVarP(&options.Flags.Verbose, "eksctl-log-level", "", -1, "set log level, use 0 to silence, 4 for debugging and 5 for debugging with AWS debug logging (default 3)")
	cmd.Flags().DurationVarP(&options.Flags.AWSOperationTimeout, "aws-api-timeout", "", 20*time.Minute, "Duration of AWS API timeout")
	cmd.Flags().StringVarP(&options.Flags.Region, "region", "r", "", "The region to use. Default: us-west-2")
	cmd.Flags().StringVarP(&options.Flags.Zones, optionZones, "z", "", "Availability Zones. Auto-select if not specified. If provided, this overrides the $EKS_AVAILABILITY_ZONES environment variable")
	cmd.Flags().StringVarP(&options.Flags.Profile, "profile", "p", "", "AWS profile to use. If provided, this overrides the AWS_PROFILE environment variable")
	cmd.Flags().StringVarP(&options.Flags.SshPublicKey, "ssh-public-key", "", "", "SSH public key to use for nodes (import from local path, or use existing EC2 key pair) (default \"~/.ssh/id_rsa.pub\")")
	cmd.Flags().StringVarP(&options.Flags.Tags, "tags", "", "", "A list of KV pairs used to tag all instance groups in AWS (eg \"Owner=John Doe,Team=Some Team\").")
	return cmd
}

// Runs the command logic (including installing required binaries, parsing options and aggregating eksctl command)
func (o *CreateClusterEKSOptions) Run() error {
	log.ConfigureLog(o.LogLevel)

	var deps []string
	d := binaryShouldBeInstalled("eksctl")
	if d != "" {
		deps = append(deps, d)
	}
	d = binaryShouldBeInstalled("heptio-authenticator-aws")
	if d != "" {
		deps = append(deps, d)
	}
	logger.Debugf("Dependencies to be installed: %s", strings.Join(deps, ", "))
	err := o.installMissingDependencies(deps)
	if err != nil {
		logger.Errorf("%v\nPlease fix the error or install manually then try again", err)
		os.Exit(-1)
	}

	flags := &o.Flags

	zones := flags.Zones
	if zones == "" {
		zones = os.Getenv("EKS_AVAILABILITY_ZONES")
	}

	args := []string{"create", "cluster", "--full-ecr-access"}
	if flags.ClusterName != "" {
		args = append(args, "--name", flags.ClusterName)

		clusterExists, err := amazon.EksClusterExists(flags.ClusterName, flags.Profile, flags.Region)
		if err != nil {
			return err
		}
		if clusterExists {
			logger.Infof("EKS cluster %s already exists.", util.ColorInfo(flags.ClusterName))
			return nil
		} else {
			stackExists, err := amazon.EksClusterObsoleteStackExists(flags.ClusterName, flags.Profile, flags.Region)
			if err != nil {
				return err
			}
			if stackExists {
				logger.Infof(
					`Cloud formation stack named %s exists in rollbacked state. At the same 
time there is no EKS cluster associated with it. This usually happens when there was an error during 
cluster provisioning. Cleaning up stack %s and recreating it with eksctl.`,
					util.ColorInfo(amazon.EksctlStackName(flags.ClusterName)), util.ColorInfo(amazon.EksctlStackName(flags.ClusterName)))
				err = amazon.CleanUpObsoleteEksClusterStack(flags.ClusterName, flags.Profile, flags.Region)
				if err != nil {
					return err
				}
			}
		}
	}

	region, err := amazon.ResolveRegion("", flags.Region)
	if err != nil {
		return err
	}
	args = append(args, "--region", region)

	if zones != "" {
		args = append(args, "--zones", zones)
	}
	if flags.Profile != "" {
		args = append(args, "--profile", flags.Profile)
	}
	if flags.SshPublicKey != "" {
		args = append(args, "--ssh-public-key", flags.SshPublicKey)
	}
	args = append(args, "--node-type", flags.NodeType)
	if flags.NodeCount >= 0 {
		args = append(args, "--nodes", strconv.Itoa(flags.NodeCount))
	}
	if flags.NodesMin >= 0 {
		args = append(args, "--nodes-min", strconv.Itoa(flags.NodesMin))
	}
	if flags.NodesMax >= 0 {
		args = append(args, "--nodes-max", strconv.Itoa(flags.NodesMax))
	}
	if flags.Verbose >= 0 {
		args = append(args, "--verbose", strconv.Itoa(flags.Verbose))
	}
	if flags.Tags != "" {
		args = append(args, "--tags", flags.Tags)
	}
	args = append(args, "--aws-api-timeout", flags.AWSOperationTimeout.String())

	logger.Info("Creating EKS cluster - this can take a while so please be patient...")
	logger.Infof("You can watch progress in the CloudFormation console: %s", util.ColorInfo("https://console.aws.amazon.com/cloudformation/"))

	logger.Debugf("Running command: %s", util.ColorInfo("eksctl "+strings.Join(args, " ")))
	if logger.GetLevel() == logger.DebugLevel {
		err = o.runCommandVerbose("eksctl", args...)
		if err != nil {
			return err
		}
		log.Blank()
	} else {
		err = o.runCommandQuietly("eksctl", args...)
		if err != nil {
			return err
		}
	}

	kubeClient, err := o.KubeClient()
	if err != nil {
		return err
	}
	err = kube.RememberRegion(kubeClient, o.currentNamespace, region)
	if err != nil {
		return err
	}

	logger.Info("Initialising cluster ...\n")
	return o.initAndInstall(EKS)
}
