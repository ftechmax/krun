package helper

import "strings"

func helperServiceArgs(kubeConfigPath, ownerName string) []string {
	args := []string{"--kubeconfig", kubeConfigPath}
	if trimmedOwnerName := strings.TrimSpace(ownerName); trimmedOwnerName != "" {
		args = append(args, "--owner", trimmedOwnerName)
	}
	return args
}
