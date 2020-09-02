package cmd

import (
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/mmmknt/fiddle-faddle/pkg/worker"
)

var (
	o = &worker.Options{
		LogLevel: "INFO",
	}
)

func NewWorkerCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worker",
		Short: "A worker starter",
		RunE: func(cmd *cobra.Command, args []string) error {
			logConfig := zap.NewProductionConfig()
			switch o.LogLevel {
			case "INFO":
				// nop
			case "DEBUG":
				logConfig.Level = zap.NewAtomicLevelAt(zapcore.DebugLevel)
			default:
				return errors.New(fmt.Sprintf("invalid LogLevel: %v", o.LogLevel))
			}
			logger, err := logConfig.Build()
			if err != nil {
				log.Printf("can't initialize zap logger: %v\n", err)
				return err
			}

			w, err := worker.NewWorker(logger, o, os.Getenv("DD_CLIENT_API_KEY"), os.Getenv("DD_CLIENT_APP_KEY"))
			if err != nil {
				return nil
			}
			defer func() {
				err = logger.Sync()
				if err != nil {
					log.Printf("can't sync logger: %v\n", err)
				}
			}()
			return w.Work()
		},
	}

	cmd.Flags().StringVar(&o.BufferDestinationHost, "bufferHost", "", "buffer destination host")
	cmd.Flags().StringVar(&o.InternalDestinationHost, "internalHost", "", "internal destination host")
	cmd.Flags().StringVar(&o.LogLevel, "logLevel", o.LogLevel, "log level")
	_ = cmd.MarkFlagRequired("bufferHost")
	_ = cmd.MarkFlagRequired("internalHost")

	return cmd
}
