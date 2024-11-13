package compose

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/portainer/portainer/pkg/libstack"

	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	"github.com/docker/compose/v2/pkg/api"
	"github.com/docker/compose/v2/pkg/compose"
	"github.com/rs/zerolog/log"
)

var mu sync.Mutex

func withCli(
	ctx context.Context,
	options libstack.Options,
	cliFn func(context.Context, *command.DockerCli) error,
) error {
	ctx = context.Background()

	cli, err := command.NewDockerCli()
	if err != nil {
		return fmt.Errorf("unable to create a Docker client: %w", err)
	}

	opts := flags.NewClientOptions()

	if options.Host != "" {
		opts.Hosts = []string{options.Host}
	}

	tempDir, err := os.MkdirTemp("", "docker-config")
	if err != nil {
		return fmt.Errorf("unable to create a temporary directory for the Docker config: %w", err)
	}
	defer os.RemoveAll(tempDir)

	opts.ConfigDir = tempDir

	mu.Lock()
	if err := cli.Initialize(opts); err != nil {
		mu.Unlock()
		return fmt.Errorf("unable to initialize the Docker client: %w", err)
	}
	mu.Unlock()
	defer cli.Client().Close()

	for _, r := range options.Registries {
		creds := cli.ConfigFile().GetCredentialsStore(r.ServerAddress)

		if err := creds.Store(r); err != nil {
			return fmt.Errorf("unable to store the Docker credentials: %w", err)
		}
	}

	return cliFn(ctx, cli)
}

func withComposeService(
	ctx context.Context,
	filePaths []string,
	options libstack.Options,
	composeFn func(api.Service, *types.Project) error,
) error {
	return withCli(ctx, options, func(ctx context.Context, cli *command.DockerCli) error {
		composeService := compose.NewComposeService(cli)

		configDetails := types.ConfigDetails{
			WorkingDir:  options.WorkingDir,
			Environment: make(map[string]string),
		}

		for _, p := range filePaths {
			configDetails.ConfigFiles = append(configDetails.ConfigFiles, types.ConfigFile{Filename: p})
		}

		envFile := make(map[string]string)

		if options.EnvFilePath != "" {
			env, err := dotenv.GetEnvFromFile(make(map[string]string), []string{options.EnvFilePath})
			if err != nil {
				return fmt.Errorf("unable to get the environment from the env file: %w", err)
			}

			maps.Copy(envFile, env)

			configDetails.Environment = env
		}

		if len(configDetails.ConfigFiles) == 0 {
			return composeFn(composeService, nil)
		}

		project, err := loader.LoadWithContext(ctx, configDetails,
			func(o *loader.Options) {
				o.SkipResolveEnvironment = true
				o.ResolvePaths = !slices.Contains(options.ConfigOptions, "--no-path-resolution")

				if options.ProjectName != "" {
					o.SetProjectName(options.ProjectName, true)
				}
			},
		)
		if err != nil {
			return fmt.Errorf("failed to load the compose file: %w", err)
		}

		if options.EnvFilePath != "" {
			// Work around compose path handling
			for i, service := range project.Services {
				for j, envFile := range service.EnvFiles {
					if !filepath.IsAbs(envFile.Path) {
						project.Services[i].EnvFiles[j].Path = filepath.Join(project.WorkingDir, envFile.Path)
					}
				}
			}

			if p, err := project.WithServicesEnvironmentResolved(true); err == nil {
				project = p
			} else {
				return fmt.Errorf("failed to resolve services environment: %w", err)
			}
		}

		return composeFn(composeService, project)
	})
}

// Deploy creates and starts containers
func (c *ComposeDeployer) Deploy(ctx context.Context, filePaths []string, options libstack.DeployOptions) error {
	return withComposeService(ctx, filePaths, options.Options, func(composeService api.Service, project *types.Project) error {
		addServiceLabels(project, false)

		var opts api.UpOptions
		if options.ForceRecreate {
			opts.Create.Recreate = api.RecreateForce
		}

		opts.Create.RemoveOrphans = options.RemoveOrphans
		opts.Start.CascadeStop = options.AbortOnContainerExit

		if err := composeService.Up(ctx, project, opts); err != nil {
			return fmt.Errorf("compose up operation failed: %w", err)
		}

		log.Info().Msg("Stack deployment successful")

		return nil
	})
}

func (c *ComposeDeployer) Run(ctx context.Context, filePaths []string, serviceName string, options libstack.RunOptions) error {
	return withComposeService(ctx, filePaths, options.Options, func(composeService api.Service, project *types.Project) error {
		addServiceLabels(project, true)

		if err := composeService.Create(ctx, project, api.CreateOptions{}); err != nil {
			return err
		}

		opts := api.RunOptions{
			AutoRemove: options.Remove,
			Command:    options.Args,
			Detach:     options.Detached,
			Service:    serviceName,
		}

		if _, err := composeService.RunOneOffContainer(ctx, project, opts); err != nil {
			return fmt.Errorf("compose run operation failed: %w", err)
		}

		log.Info().Msg("Stack run successful")

		return nil
	})
}

// Remove stops and removes containers
func (c *ComposeDeployer) Remove(ctx context.Context, projectName string, filePaths []string, options libstack.RemoveOptions) error {
	if err := withCli(ctx, options.Options, func(ctx context.Context, cli *command.DockerCli) error {
		composeService := compose.NewComposeService(cli)

		return composeService.Down(ctx, projectName, api.DownOptions{RemoveOrphans: true, Volumes: options.Volumes})
	}); err != nil {
		return fmt.Errorf("compose down operation failed: %w", err)
	}

	log.Info().Msg("Stack removal successful")

	return nil
}

// Pull pulls images
func (c *ComposeDeployer) Pull(ctx context.Context, filePaths []string, options libstack.Options) error {
	if err := withComposeService(ctx, filePaths, options, func(composeService api.Service, project *types.Project) error {
		return composeService.Pull(ctx, project, api.PullOptions{})
	}); err != nil {
		return fmt.Errorf("compose pull operation failed: %w", err)
	}

	log.Info().Msg("Stack pull successful")

	return nil
}

// Validate validates stack file
func (c *ComposeDeployer) Validate(ctx context.Context, filePaths []string, options libstack.Options) error {
	return withComposeService(ctx, filePaths, options, func(composeService api.Service, project *types.Project) error {
		return nil
	})
}

func (c *ComposeDeployer) Config(ctx context.Context, filePaths []string, options libstack.Options) ([]byte, error) {
	var payload []byte

	if err := withComposeService(ctx, filePaths, options, func(composeService api.Service, project *types.Project) error {
		var err error
		payload, err = project.MarshalYAML()
		if err != nil {
			return fmt.Errorf("unable to marshal as YAML: %w", err)
		}

		return nil
	}); err != nil {
		return nil, fmt.Errorf("compose config operation failed: %w", err)
	}

	return payload, nil
}

func addServiceLabels(project *types.Project, oneOff bool) {
	oneOffLabel := "False"
	if oneOff {
		oneOffLabel = "True"
	}

	for i, s := range project.Services {
		s.CustomLabels = map[string]string{
			api.ProjectLabel:     project.Name,
			api.ServiceLabel:     s.Name,
			api.VersionLabel:     api.ComposeVersion,
			api.WorkingDirLabel:  "/",
			api.ConfigFilesLabel: strings.Join(project.ComposeFiles, ","),
			api.OneoffLabel:      oneOffLabel,
		}
		project.Services[i] = s
	}
}
