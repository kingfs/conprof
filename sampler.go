package main

import (
	"context"
	"fmt"
	"net/http"

	"github.com/Go-SIP/conprof/config"
	"github.com/Go-SIP/conprof/scrape"
	"github.com/Go-SIP/conprof/storage"
	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/oklog/run"
	opentracing "github.com/opentracing/opentracing-go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/prometheus/discovery/targetgroup"
	kingpin "gopkg.in/alecthomas/kingpin.v2"
)

// registerSampler registers a sampler command.
func registerSampler(m map[string]setupFunc, app *kingpin.Application, name string) {
	cmd := app.Command(name, "sampler")

	storagePath := cmd.Flag("storage.path", "Directory to read storage from.").
		Default("./data").String()
	configFile := cmd.Flag("config.file", "Config file to use.").
		Default("conprof.yaml").String()

	m[name] = func(g *run.Group, mux *http.ServeMux, logger log.Logger, reg *prometheus.Registry, tracer opentracing.Tracer, debugLogging bool) error {
		return runSampler(g, logger, *storagePath, *configFile)
	}
}

func runSampler(g *run.Group, logger log.Logger, storagePath, configFile string) error {
	storage := storage.NewDiskStorage(log.With(logger, "component", "storage"), storagePath)
	scrapeManager := scrape.NewManager(log.With(logger, "component", "scrape-manager"), storage)
	c, err := config.LoadFile(configFile)
	if err != nil {
		return fmt.Errorf("could not load config: %v", err)
	}

	syncCh := make(chan map[string][]*targetgroup.Group)

	{
		_, cancel := context.WithCancel(context.Background())
		g.Add(func() error {
			err = scrapeManager.ApplyConfig(c)
			if err != nil {
				return fmt.Errorf("could not apply config: %v", err)
			}
			scrapeManager.Run(syncCh)

			return nil
		}, func(error) {
			level.Debug(logger).Log("msg", "shutting down scrape manager")
			scrapeManager.Stop()
			cancel()
			close(syncCh)
		})
	}
	{
		ctx, cancel := context.WithCancel(context.Background())
		g.Add(func() error {
			for _, sc := range c.ScrapeConfigs {
				select {
				case <-ctx.Done():
					return nil
				case syncCh <- map[string][]*targetgroup.Group{sc.JobName: sc.ServiceDiscoveryConfig.StaticConfigs}:
					// continue
				}
			}
			<-ctx.Done()

			return nil
		}, func(error) {
			level.Debug(logger).Log("msg", "shutting down discovery")
			cancel()
		})
	}
	return nil
}
