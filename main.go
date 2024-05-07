package main

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"connectrpc.com/connect"
	agentv1 "github.com/grafana/alloy-remote-config/api/gen/proto/go/collector/v1"
	"github.com/grafana/alloy-remote-config/api/gen/proto/go/collector/v1/collectorv1connect"
	pipev1 "github.com/grafana/alloy-remote-config/api/gen/proto/go/pipeline/v1"
	"github.com/grafana/alloy-remote-config/api/gen/proto/go/pipeline/v1/pipelinev1connect"
)

var flagDir = flag.String("d", ".", "Directory to look for pipeline files")
var flagPurge = flag.Bool("purge", false, "If set, delete any remote pipelines not found locally")

// todo:
var flagBackupDir = flag.String("backup", "", "If set, download remote configs to this directory and exit")

type mytransport struct {
	username string
	token    string
}

func (m *mytransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.SetBasicAuth(m.username, m.token)
	return http.DefaultTransport.RoundTrip(req)
}

func main() {
	flag.Parse()
	pipes, err := loadPipelinesFromFiles()
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Loaded %d local pipelines", len(pipes))

	host := os.Getenv("FLEET_MANAGEMENT_HOST")
	user := os.Getenv("FLEET_MANAGEMENT_USER")
	token := os.Getenv("FLEET_MANAGEMENT_TOKEN")
	if host == "" || user == "" {
		log.Fatal("FLEET_MANAGEMENT_HOST and FLEET_MANAGEMENT_USER are required")
	}

	hclient := &http.Client{
		Transport: &mytransport{
			username: user,
			token:    token,
		},
	}

	pipeclient := pipelinev1connect.NewPipelineServiceClient(
		hclient,
		host,
	)

	resp, err := pipeclient.ListPipelines(
		context.Background(),
		connect.NewRequest(&pipev1.ListPipelinesRequest{}),
	)
	if err != nil {
		log.Fatal(err)
	}
	remoteNames := map[string]bool{}
	localNames := map[string]bool{}
	log.Printf("Found %d remote pipelines", len(resp.Msg.GetPipelines()))

	if *flagBackupDir != "" {
		os.RemoveAll(*flagBackupDir)
		os.MkdirAll(*flagBackupDir, 0764)
		for _, p := range resp.Msg.Pipelines {
			writePipelineToFile(*flagBackupDir, p)
		}
		os.Exit(0)
	}

	for _, p := range resp.Msg.Pipelines {
		remoteNames[p.Name] = true
	}
	for _, p := range pipes {
		localNames[p.Name] = true
	}
	for _, p := range pipes {
		if remoteNames[p.Name] {
			// update
			log.Printf("Updating pipeline %s", p.Name)
			_, err := pipeclient.UpdatePipeline(
				context.Background(),
				connect.NewRequest(&pipev1.UpdatePipelineRequest{
					Pipeline: p,
				}),
			)
			if err != nil {
				log.Println(err)
			}
		} else {
			// create
			log.Printf("Creating pipeline %s", p.Name)
			_, err := pipeclient.CreatePipeline(
				context.Background(),
				connect.NewRequest(&pipev1.CreatePipelineRequest{
					Pipeline: p,
				}),
			)
			if err != nil {
				log.Println(err)
			}
		}
	}
	// delete anything not found locally
	if *flagPurge {
		for name := range remoteNames {
			if !localNames[name] {
				log.Printf("Deleting remote pipeline %s.", name)
				_, err = pipeclient.DeletePipeline(
					context.Background(),
					connect.NewRequest(&pipev1.DeletePipelineRequest{
						Name: name,
					}),
				)
				if err != nil {
					log.Fatal(err)
				}
			}
		}
	}

	agentclient := collectorv1connect.NewCollectorServiceClient(
		hclient,
		host,
	)
	resp3, err := agentclient.GetConfig(
		context.Background(),
		connect.NewRequest(&agentv1.GetConfigRequest{
			Id:       "abc",
			Metadata: map[string]string{"os": "linux"},
		}),
	)
	if err != nil {
		log.Fatal(err)
	}
	os.WriteFile("run.alloy", []byte(resp3.Msg.Content), 0644)
}

var matchersRegex = regexp.MustCompile(`(?m)^\s*//\s*matchers?:?\s+(.*)$`)

func loadPipelinesFromFiles() ([]*pipev1.Pipeline, error) {
	pipes := []*pipev1.Pipeline{}
	err := filepath.Walk(*flagDir, func(path string, info fs.FileInfo, err error) error {
		if info.IsDir() {
			return nil
		}
		if err != nil {
			return err
		}
		ext := filepath.Ext(path)
		if ext != ".alloy" && ext != ".river" {
			return nil
		}
		pipeName := strings.TrimSuffix(filepath.Base(path), ext)
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		matches := matchersRegex.FindAllStringSubmatch(string(content), -1)
		if len(matches) > 1 {
			return fmt.Errorf("%s: Only one matchers comment allowed", path)
		}
		matchers := []string{}
		if len(matches) == 1 {
			matchers = strings.Split(strings.TrimSpace(matches[0][1]), " ")
		}
		pipe := &pipev1.Pipeline{
			Matchers: matchers,
			Contents: matchersRegex.ReplaceAllString(string(content), ""),
			Name:     pipeName,
		}
		pipes = append(pipes, pipe)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return pipes, nil
}

func writePipelineToFile(dir string, p *pipev1.Pipeline) {
	path := filepath.Join(dir, p.Name+".alloy")
	content := p.Contents
	if len(p.Matchers) > 0 {
		parts := []string{"// matchers:"}
		parts = append(parts, p.Matchers...)
		content = strings.Join(parts, " ") + "\n" + content
	}
	log.Println(path)
	err := os.WriteFile(path, []byte(content), 0664)
	if err != nil {
		log.Printf("Error writing %s: %s", path, err)
	}
}
