package cmd

import (
	"context"

	_ "github.com/go-sql-driver/mysql" // mysql driver
	"github.com/pingcap/errors"
	"github.com/pingcap/ticdc/cdc/model"
	"github.com/spf13/cobra"
)

func newProcessorCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "processor",
		Short: "Manage processor (processor is a sub replication task running on a specified capture)",
	}
	command.AddCommand(
		newListProcessorCommand(),
		newQueryProcessorCommand(),
	)
	return command
}

func newListProcessorCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "list",
		Short: "List all processors in TiCDC cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			info, err := cdcEtcdCli.GetProcessors(context.Background())
			if err != nil {
				return err
			}
			return jsonPrint(cmd, info)
		},
	}
	return command
}

func newQueryProcessorCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "query",
		Short: "Query information and status of a sub replication task (processor)",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, status, err := cdcEtcdCli.GetTaskStatus(context.Background(), changefeedID, captureID)
			if err != nil && errors.Cause(err) != model.ErrTaskStatusNotExists {
				return err
			}
			_, position, err := cdcEtcdCli.GetTaskPosition(context.Background(), changefeedID, captureID)
			if err != nil && errors.Cause(err) != model.ErrTaskPositionNotExists {
				return err
			}
			meta := &processorMeta{Status: status, Position: position}
			return jsonPrint(cmd, meta)
		},
	}
	command.PersistentFlags().StringVar(&changefeedID, "changefeed-id", "", "Replication task (changefeed) ID")
	command.PersistentFlags().StringVar(&captureID, "capture-id", "", "Capture ID")
	_ = command.MarkPersistentFlagRequired("changefeed-id")
	_ = command.MarkPersistentFlagRequired("capture-id")
	return command
}
