package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	cfg "github.com/ftechmax/krun/internal/config"
	"github.com/ftechmax/krun/internal/contracts"
	"github.com/ftechmax/krun/internal/krun/helperclient"
	"github.com/ftechmax/krun/internal/utils"
	"github.com/spf13/cobra"
)

var (
	version = "debug" // will be set by the build system
	config  = cfg.Config{}
)

var (
	kubeConfigPath string
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "krun",
		Short: "krun CLI",
		Long:  `krun [global options] <command> [command options] <service>`,
	}
	rootCmd.PersistentFlags().StringVar(&kubeConfigPath, "kubeconfig", "", "Path to kubeconfig file")

	rootCmd.AddCommand(
		&cobra.Command{
			Use:   "version",
			Short: "Show version information",
			Run: func(cmd *cobra.Command, args []string) {
				fmt.Println(version)
			},
		},
		&cobra.Command{
			Use:     "list",
			Short:   "List all services or projects",
			PreRun:  preRunInit,
			Run:     handleList,
			Example: "krun list",
		},
		&cobra.Command{
			Use:     "status",
			Short:   "Show status of daemon and traffic-manager",
			Run:     handleStatus,
			Example: "krun status",
		},
	)

	buildCmd := &cobra.Command{
		Use:    "build <project|service>",
		Short:  "Build a project or specific service",
		Args:   cobra.MinimumNArgs(1),
		PreRun: preRunInit,
		Run:    handleBuild,
	}
	buildCmd.Flags().Bool("skip-web", false, "Skip building the web component")
	buildCmd.Flags().Bool("force", false, "Force build even if up to date")
	buildCmd.Flags().Bool("flush", false, "Delete build cache")
	rootCmd.AddCommand(buildCmd)

	deployCmd := &cobra.Command{
		Use:    "deploy <project>",
		Short:  "Deploy a project",
		Args:   cobra.MinimumNArgs(1),
		PreRun: preRunInit,
		Run:    handleDeploy,
	}
	deployCmd.Flags().Bool("use-remote-registry", false, "Use remote registry for deploy")
	deployCmd.Flags().Bool("no-restart", false, "Skip rollout restart after apply")
	rootCmd.AddCommand(deployCmd)

	deleteCmd := &cobra.Command{
		Use:     "delete <project>",
		Short:   "Delete a project",
		Args:    cobra.MinimumNArgs(1),
		PreRun:  preRunInit,
		Run:     handleDelete,
		Example: "krun delete myproject",
	}
	rootCmd.AddCommand(deleteCmd)

	debugCmd := &cobra.Command{
		Use:              "debug",
		Short:            "Debug commands",
		PersistentPreRun: preRunInit,
	}
	debugListCmd := &cobra.Command{
		Use:   "list",
		Short: "List active debug sessions",
		Run:   handleDebugList,
	}
	debugEnableCmd := &cobra.Command{
		Use:   "enable <service>",
		Short: "Enable debug mode for a service",
		Args:  cobra.MinimumNArgs(1),
		Run:   handleDebugEnable,
	}
	debugEnableCmd.Flags().String("container", "", "Name of the target container in the workload")
	debugDisableCmd := &cobra.Command{
		Use:   "disable <service>",
		Short: "Disable debug mode for a service",
		Args:  cobra.MinimumNArgs(1),
		Run:   handleDebugDisable,
	}
	debugHelperCmd := &cobra.Command{
		Use:   "helper",
		Short: "Inspect local debug helper daemon",
	}

	debugCmd.AddCommand(debugListCmd, debugEnableCmd, debugDisableCmd, debugHelperCmd)
	rootCmd.AddCommand(debugCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(utils.Colorize(err.Error(), utils.Red))
		os.Exit(1)
	}
}

func preRunInit(cmd *cobra.Command, args []string) {
	initialize(kubeConfigPath)
}

func initialize(optKubeConfig string) {
	if optKubeConfig != "" {
		config.KubeConfig = filepath.ToSlash(optKubeConfig)
	} else {
		dirname, err := os.UserHomeDir()
		if err != nil {
			fmt.Println(utils.Colorize(fmt.Sprintf("Error getting user home directory: %s", err), utils.Red))
			os.Exit(0)
		}
		config.KubeConfig = filepath.ToSlash(dirname + "/.kube/config")
	}

	krunConfig, err := cfg.LoadKrunConfig()
	if err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("Error parsing config.json: %s", err), utils.Red))
		os.Exit(1)
	}

	token, err := cfg.LoadToken()
	if err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("Error loading auth token: %s", err), utils.Red))
		os.Exit(1)
	}

	config.KrunConfig = krunConfig
	config.Registry = config.LocalRegistry
	config.AuthToken = token

}

func handleStatus(cmd *cobra.Command, args []string) {

}

func handleList(cmd *cobra.Command, args []string) {
	services, projects, err := helperclient.List(config)
	if err != nil {
		fmt.Println(utils.Colorize(err.Error(), utils.Red))
		os.Exit(1)
	}

	if len(services) == 0 {
		fmt.Println(utils.Colorize("No services found", utils.Yellow))
		return
	}

	fmt.Println("Services")
	fmt.Println("--------")
	for _, service := range services {
		fmt.Println(service)
	}
	fmt.Println("")

	fmt.Println("Projects")
	fmt.Println("--------")
	for _, project := range projects {
		fmt.Println(project)
	}
	fmt.Println("")
}

func handleBuild(cmd *cobra.Command, args []string) {
	skipWeb, _ := cmd.Flags().GetBool("skip-web")
	forceBuild, _ := cmd.Flags().GetBool("force")
	flush, _ := cmd.Flags().GetBool("flush")

	err := helperclient.Build(config, contracts.BuildRequest{
		KubeConfig: config.KubeConfig,
		Target:     args[0],
		SkipWeb:    skipWeb,
		Force:      forceBuild,
		Flush:      flush,
	}, os.Stdout)
	if err != nil {
		fmt.Println(utils.Colorize(err.Error(), utils.Red))
		os.Exit(1)
	}
}

func handleDeploy(cmd *cobra.Command, args []string) {
	useRemote, _ := cmd.Flags().GetBool("use-remote-registry")
	noRestart, _ := cmd.Flags().GetBool("no-restart")

	err := helperclient.Deploy(config, contracts.DeployRequest{
		KubeConfig:        config.KubeConfig,
		Target:            args[0],
		UseRemoteRegistry: useRemote,
		NoRestart:         noRestart,
	}, os.Stdout)
	if err != nil {
		fmt.Println(utils.Colorize(err.Error(), utils.Red))
		os.Exit(1)
	}
}

func handleDelete(cmd *cobra.Command, args []string) {
	err := helperclient.Delete(config, contracts.DeleteRequest{
		KubeConfig: config.KubeConfig,
		Target:     args[0],
	}, os.Stdout)
	if err != nil {
		fmt.Println(utils.Colorize(err.Error(), utils.Red))
		os.Exit(1)
	}
}

func handleDebugList(cmd *cobra.Command, args []string) {
	sessions, err := helperclient.DebugSessionsList(config)
	if err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("cannot list debug sessions via helper: %v", err), utils.Red))
		return
	}

	if len(sessions) == 0 {
		fmt.Println(utils.Colorize("No active debug sessions", utils.Yellow))
		return
	}

	fmt.Println("Active debug sessions")
	fmt.Println("---------------------")
	for _, session := range sessions {
		serviceName := strings.TrimSpace(session.Context.ServiceName)
		namespace := strings.TrimSpace(session.Context.Namespace)
		if namespace == "" {
			namespace = "default"
		}

		fmt.Printf("Service: %s (namespace: %s)\n", serviceName, namespace)
		fmt.Printf("Intercept port: %d\n", session.Context.InterceptPort)
		fmt.Println("Service dependencies:")

		if len(session.Context.ServiceDependencies) == 0 {
			fmt.Println("  (none)")
		} else {
			for _, dependency := range session.Context.ServiceDependencies {
				host := strings.TrimSpace(dependency.Host)
				if host == "" {
					service := strings.TrimSpace(dependency.Service)
					dependencyNamespace := strings.TrimSpace(dependency.Namespace)
					if dependencyNamespace == "" {
						dependencyNamespace = "default"
					}
					if service == "" {
						host = "(unknown)"
					} else {
						host = fmt.Sprintf("%s.%s.svc", service, dependencyNamespace)
					}
				}
				fmt.Printf("  - %s:%d\n", host, dependency.Port)
			}
		}
		fmt.Println("")
	}
}

func handleDebugEnable(cmd *cobra.Command, args []string) {
	containerName, _ := cmd.Flags().GetString("container")

	service, err := helperclient.GetServiceByName(config, args[0])
	if err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("service not found: %s", args[0]), utils.Red))
		return
	}

	fmt.Printf("Enabling debug mode for service %s\n", service.Name)

	response, err := helperclient.DebugEnable(config, service, containerName)
	if err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("cannot apply debug enable via helper: %v", err), utils.Red))
		return
	}
	if !response.Success {
		fmt.Println(utils.Colorize(fmt.Sprintf("helper refused debug enable: %s", response.Message), utils.Red))
		return
	}

	fmt.Println(utils.Colorize("Session created", utils.Green))

	// TODO: move to krun-helper
	// if err := deploy.CreateEnvFile(service, config, containerName); err != nil {
	// 	fmt.Println(utils.Colorize(fmt.Sprintf("warning: could not create env file: %v", err), utils.Yellow))
	// }
}

func handleDebugDisable(cmd *cobra.Command, args []string) {

	service, err := helperclient.GetServiceByName(config, args[0])
	if err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("service not found: %s", args[0]), utils.Red))
		return
	}

	fmt.Printf("Disabling debug mode for service %s\n", service.Name)

	response, err := helperclient.DebugDisable(config, service)
	if err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("cannot apply debug disable via helper: %v", err), utils.Red))
		return
	}
	if !response.Success {
		fmt.Println(utils.Colorize(fmt.Sprintf("helper refused debug disable: %s", response.Message), utils.Red))
		return
	}

	if response.Message == "no active session" {
		fmt.Println(utils.Colorize("No active debug session found", utils.Yellow))
	} else {
		fmt.Println(utils.Colorize("Session removed", utils.Green))

		// TODO: move to krun-helper
		// if err := deploy.RemoveEnvFile(service, config); err != nil {
		// 	fmt.Println(utils.Colorize(fmt.Sprintf("warning: could not remove env file: %v", err), utils.Yellow))
		// }
	}
}
