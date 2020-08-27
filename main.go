package main

import (
	"github.com/spf13/cobra"

	"github.com/mmmknt/fiddle-faddle/pkg/cmd"
)

func main() {
	rootCmd := &cobra.Command{Use: "app"}
	rootCmd.AddCommand(cmd.NewWorker())
	rootCmd.Execute()
}
