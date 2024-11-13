package compose

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portainer/portainer/pkg/libstack"

	"github.com/stretchr/testify/require"
)

func Test_UpAndDown(t *testing.T) {
	const projectName = "composetest"

	const composeFileContent = `version: "3.9"
services:
  busybox:
    image: "alpine:3.7"
    container_name: "composetest_container_one"`

	const overrideComposeFileContent = `version: "3.9"
services:
  busybox:
    image: "alpine:latest"
    container_name: "composetest_container_two"`

	composeContainerName := projectName + "_container_two"

	w := NewComposeDeployer()

	dir := t.TempDir()

	filePathOriginal, err := createFile(dir, "docker-compose.yml", composeFileContent)
	require.NoError(t, err)

	filePathOverride, err := createFile(dir, "docker-compose-override.yml", overrideComposeFileContent)
	require.NoError(t, err)

	filePaths := []string{filePathOriginal, filePathOverride}

	ctx := context.Background()

	err = w.Validate(ctx, filePaths, libstack.Options{ProjectName: projectName})
	require.NoError(t, err)

	err = w.Pull(ctx, filePaths, libstack.Options{ProjectName: projectName})
	require.NoError(t, err)

	require.False(t, containerExists(composeContainerName))

	err = w.Deploy(ctx, filePaths, libstack.DeployOptions{
		Options: libstack.Options{
			ProjectName: projectName,
		},
	})
	require.NoError(t, err)

	require.True(t, containerExists(composeContainerName))

	waitResult := <-w.WaitForStatus(ctx, projectName, libstack.StatusCompleted)

	require.Empty(t, waitResult.ErrorMsg)
	require.Equal(t, libstack.StatusCompleted, waitResult.Status)

	err = w.Remove(ctx, projectName, filePaths, libstack.RemoveOptions{})
	require.NoError(t, err)

	require.False(t, containerExists(composeContainerName))
}

func createFile(dir, fileName, content string) (string, error) {
	filePath := filepath.Join(dir, fileName)

	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return "", err
	}

	return filePath, nil
}

func containerExists(containerName string) bool {
	cmd := exec.Command("docker", "ps", "-a", "-f", "name="+containerName)

	out, err := cmd.Output()
	if err != nil {
		log.Fatalf("failed to list containers: %s", err)
	}

	return strings.Contains(string(out), containerName)
}

func Test_Validate(t *testing.T) {
	invalidComposeFileContent := `invalid-file-content`

	w := NewComposeDeployer()

	dir := t.TempDir()

	filePathOriginal, err := createFile(dir, "docker-compose.yml", invalidComposeFileContent)
	require.NoError(t, err)

	filePaths := []string{filePathOriginal}

	projectName := "plugintest"

	ctx := context.Background()

	err = w.Validate(ctx, filePaths, libstack.Options{ProjectName: projectName})
	require.Error(t, err)
}

func Test_Config(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	projectName := "configtest"

	defer os.RemoveAll(dir)

	testCases := []struct {
		name               string
		composeFileContent string
		expectFileContent  string
		envFileContent     string
	}{
		{
			name: "compose file with relative path",
			composeFileContent: `services:
  app:
    image: 'nginx:latest'
    ports:
      - '80:80'
    volumes:
      - ./nginx-data:/data`,
			expectFileContent: `name: configtest
services:
  app:
    image: nginx:latest
    networks:
      default: null
    ports:
      - mode: ingress
        target: 80
        published: "80"
        protocol: tcp
    volumes:
      - type: bind
        source: ./nginx-data
        target: /data
        bind:
          create_host_path: true
networks:
  default:
    name: configtest_default
`,
			envFileContent: "",
		},
		{
			name: "compose file with absolute path",
			composeFileContent: `services:
  app:
    image: 'nginx:latest'
    ports:
      - '80:80'
    volumes:
      - /nginx-data:/data`,
			expectFileContent: `name: configtest
services:
  app:
    image: nginx:latest
    networks:
      default: null
    ports:
      - mode: ingress
        target: 80
        published: "80"
        protocol: tcp
    volumes:
      - type: bind
        source: /nginx-data
        target: /data
        bind:
          create_host_path: true
networks:
  default:
    name: configtest_default
`,
			envFileContent: "",
		},
		{
			name: "compose file with declared volume",
			composeFileContent: `services:
  app:
    image: 'nginx:latest'
    ports:
      - '80:80'
    volumes:
      - nginx-data:/data
volumes:
  nginx-data:
    driver: local`,
			expectFileContent: `name: configtest
services:
  app:
    image: nginx:latest
    networks:
      default: null
    ports:
      - mode: ingress
        target: 80
        published: "80"
        protocol: tcp
    volumes:
      - type: volume
        source: nginx-data
        target: /data
        volume: {}
networks:
  default:
    name: configtest_default
volumes:
  nginx-data:
    name: configtest_nginx-data
    driver: local
`,
			envFileContent: "",
		},
		{
			name: "compose file with relative path environment variable placeholder",
			composeFileContent: `services:
  nginx:
    image: nginx:latest
    ports:
      - 8019:80
    volumes:
      - ${WEB_HOME}:/usr/share/nginx/html/
    env_file:
      - stack.env
`,
			expectFileContent: `name: configtest
services:
  nginx:
    environment:
      WEB_HOME: ./html
    image: nginx:latest
    networks:
      default: null
    ports:
      - mode: ingress
        target: 80
        published: "8019"
        protocol: tcp
    volumes:
      - type: bind
        source: ./html
        target: /usr/share/nginx/html
        bind:
          create_host_path: true
networks:
  default:
    name: configtest_default
`,
			envFileContent: `WEB_HOME=./html`,
		},
		{
			name: "compose file with absolute path environment variable placeholder",
			composeFileContent: `services:
  nginx:
    image: nginx:latest
    ports:
      - 8019:80
    volumes:
      - ${WEB_HOME}:/usr/share/nginx/html/
    env_file:
      - stack.env
`,

			expectFileContent: `name: configtest
services:
  nginx:
    environment:
      WEB_HOME: /usr/share/nginx/html
    image: nginx:latest
    networks:
      default: null
    ports:
      - mode: ingress
        target: 80
        published: "8019"
        protocol: tcp
    volumes:
      - type: bind
        source: /usr/share/nginx/html
        target: /usr/share/nginx/html
        bind:
          create_host_path: true
networks:
  default:
    name: configtest_default
`,
			envFileContent: `WEB_HOME=/usr/share/nginx/html`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			composeFilePath, err := createFile(dir, "docker-compose.yml", tc.composeFileContent)
			require.NoError(t, err)

			envFilePath := ""
			if tc.envFileContent != "" {
				envFilePath, err = createFile(dir, "stack.env", tc.envFileContent)
				require.NoError(t, err)
			}

			w := NewComposeDeployer()
			actual, err := w.Config(ctx, []string{composeFilePath}, libstack.Options{
				WorkingDir:    dir,
				ProjectName:   projectName,
				EnvFilePath:   envFilePath,
				ConfigOptions: []string{"--no-path-resolution"},
			})
			require.NoError(t, err)

			require.Equal(t, tc.expectFileContent, string(actual))
		})
	}
}
