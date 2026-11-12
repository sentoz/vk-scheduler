package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jessevdk/go-flags"
	"github.com/rs/zerolog/log"
	"github.com/sentoz/vk-scheduler/internal/app"
	"github.com/sentoz/vk-scheduler/internal/config"
	"github.com/sentoz/vk-scheduler/internal/logger"
	"github.com/sentoz/vk-scheduler/internal/vars"
	"github.com/sentoz/vk-scheduler/internal/vkcloud"
)

type options struct {
	Config   string        `short:"c" long:"config" env:"VK_SCHEDULER_CONFIG" default:"config.yaml" description:"Configuration YAML or JSON file"`
	DryRun   bool          `short:"n" long:"dry-run" description:"Read cloud state without resource mutations"`
	Version  bool          `long:"version" description:"Print build metadata"`
	AuthType string        `long:"auth-type" env:"OS_AUTH_TYPE" default:"v3password" description:"Keystone mode (v3password or v3token)"`
	Log      logger.Logger `group:"Logging"`
}

func run(args []string) error {
	var options options
	parser := flags.NewParser(&options, flags.HelpFlag|flags.PassDoubleDash)
	rest, err := parser.ParseArgs(args)
	if err != nil {
		var flagErr *flags.Error
		if errors.As(err, &flagErr) && flagErr.Type == flags.ErrHelp {
			parser.WriteHelp(os.Stdout)
			return nil
		}
		return err
	}
	if len(rest) > 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	if options.Version {
		vars.Print()
		return nil
	}
	if err := options.Log.Setup(); err != nil {
		return err
	}
	cfg, err := config.Load(options.Config)
	if err != nil {
		return err
	}
	bundle, err := config.CaptureBundle(cfg.SchedulesDir)
	if err != nil {
		return err
	}
	items, err := config.ParseBundle(bundle, cfg.Cloud)
	if err != nil {
		return err
	}
	location, err := cfg.Location()
	if err != nil {
		return err
	}
	compiled, err := config.CompileSnapshot(0, items, location)
	if err != nil {
		return err
	}
	if err := compiled.ValidateCalendars(time.Now()); err != nil {
		return err
	}
	resources := make([]config.Resource, 0, len(items))
	for _, item := range items {
		resources = append(resources, item.Resource)
	}
	auth, err := vkcloud.AuthFromEnv(options.AuthType)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	client, err := vkcloud.NewClient(ctx, cfg.Cloud, auth, resources, cfg.OperationTimeout.Std())
	if err != nil {
		return err
	}
	application, err := app.New(*cfg, client, app.Options{DryRun: options.DryRun})
	if err != nil {
		return errors.Join(err, client.Shutdown(ctx))
	}
	return application.Run(ctx)
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Error().Err(err).Msg("VK scheduler stopped")
		os.Exit(1)
	}
}
