//go:build !darwin

package fsx

func RunMutationHelperMode(args []string) (handled bool, code int) {
	if len(args) == 1 && args[0] == "--pmcg-mutation-helper-v1" {
		return true, 1
	}
	return false, 0
}
