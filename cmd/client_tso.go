package cmd

import (
	"context"
	"os"

	"github.com/pingcap/tidb/store/tikv/oracle"
	"github.com/spf13/cobra"
)

func newTsoCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "tso",
		Short: "Manage tso",
	}
	command.AddCommand(
		newQueryTsoCommand(),
	)
	return command
}

func newQueryTsoCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "query",
		Short: "Get tso from PD",
		RunE: func(cmd *cobra.Command, args []string) error {
			ts, logic, err := pdCli.GetTS(context.Background())
			if err != nil {
				return err
			}
			cmd.Println(oracle.ComposeTS(ts, logic))
			return nil
		},
	}
	command.SetOutput(os.Stdout)
	return command
}
