/*******************************************************************************
 * Copyright (c) 2019 IBM Corporation and others.
 * All rights reserved. This program and the accompanying materials
 * are made available under the terms of the Eclipse Public License v2.0
 * which accompanies this distribution, and is available at
 * http://www.eclipse.org/legal/epl-v20.html
 *
 * Contributors:
 *     IBM Corporation - initial API and implementation
 *******************************************************************************/

package utils

import (
	"bytes"
	"context"
	"encoding/json"
	goErr "errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/jsonmessage"
	"github.com/docker/docker/pkg/term"
	logr "github.com/sirupsen/logrus"
)

const pfeImageName = "eclipse/codewind-pfe"
const performanceImageName = "eclipse/codewind-performance"

var baseImageNameArr = [2]string{
	pfeImageName,
	performanceImageName,
}

const pfeContainerName = "codewind-pfe"
const performanceContainerName = "codewind-performance"

var containerImageNames = [...]string{
	pfeContainerName,
	performanceContainerName,
}

// codewind-docker-compose.yaml data
var data = `
version: 2
services:
 ` + pfeContainerName + `:
  image: ${PFE_IMAGE_NAME}${PLATFORM}:${TAG}
  container_name: codewind-pfe
  user: root
  environment: [
    "HOST_WORKSPACE_DIRECTORY=${WORKSPACE_DIRECTORY}",
    "CONTAINER_WORKSPACE_DIRECTORY=/codewind-workspace",
    "HOST_OS=${HOST_OS}","CODEWIND_VERSION=${TAG}",
    "PERFORMANCE_CONTAINER=codewind-performance${PLATFORM}:${TAG}",
    "HOST_HOME=${HOST_HOME}",
    "HOST_MAVEN_OPTS=${HOST_MAVEN_OPTS}",
    "LOG_LEVEL=${LOG_LEVEL}"
  ]
  depends_on: [codewind-performance]
  ports: ["127.0.0.1:${PFE_EXTERNAL_PORT}:9090"]
  volumes: ["/var/run/docker.sock:/var/run/docker.sock","cw-workspace:/codewind-workspace","${WORKSPACE_DIRECTORY}:/mounted-workspace"]
  networks: [network]
 ` + performanceContainerName + `:
  image: ${PERFORMANCE_IMAGE_NAME}${PLATFORM}:${TAG}
  ports: ["127.0.0.1:9095:9095"]
  container_name: codewind-performance
  networks: [network]
networks:
  network:
   driver_opts:
    com.docker.network.bridge.host_binding_ipv4: "127.0.0.1"
volumes:
  cw-workspace:
`

// Compose struct for the docker compose yaml file
type Compose struct {
	Version  string `yaml:"version"`
	SERVICES struct {
		PFE struct {
			Image         string   `yaml:"image"`
			ContainerName string   `yaml:"container_name"`
			User          string   `yaml:"user"`
			Environment   []string `yaml:"environment"`
			DependsOn     []string `yaml:"depends_on"`
			Ports         []string `yaml:"ports"`
			Volumes       []string `yaml:"volumes"`
			Networks      []string `yaml:"networks"`
		} `yaml:"codewind-pfe"`
		PERFORMANCE struct {
			Image         string   `yaml:"image"`
			Ports         []string `yaml:"ports"`
			ContainerName string   `yaml:"container_name"`
			Volumes       []string `yaml:"volumes"`
			Networks      []string `yaml:"networks"`
		} `yaml:"codewind-performance"`
	} `yaml:"services"`
	VOLUME struct {
		CodewindWorkspace map[string]string `yaml:"cw-workspace"`
	} `yaml:"volumes"`
	NETWORKS struct {
		NETWORK struct {
			DRIVEROPTS struct {
				HostIP string `yaml:"com.docker.network.bridge.host_binding_ipv4"`
			} `yaml:"driver_opts"`
		} `yaml:"network"`
	} `yaml:"networks"`
}

// constant to identify the internal port of PFE in its container
const internalPFEPort = 9090

// constants to identify the range of external ports on which to expose PFE
const (
	minTCPPort   = 10000
	maxTCPPort   = 11000
	minDebugPort = 34000
	maxDebugPort = 35000
)

// DockerCompose to set up the Codewind environment
func DockerCompose(dockerComposeFile string, tag string, loglevel string) *DockerError {
	setupDockerComposeEnvs(tag, "", loglevel)
	cmd := exec.Command("docker-compose", "-f", dockerComposeFile, "up", "-d", "--force-recreate")
	output := new(bytes.Buffer)
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Start(); err != nil { // after 'Start' the program is continued and script is executing in background
	    //print out docker-compose sysout & syserr for error diagnosis
		fmt.Printf(output.String())
		DeleteTempFile(dockerComposeFile)
		return &DockerError{errOpDockerComposeStart, err, err.Error()}
	}
	fmt.Printf("Please wait while containers initialize... %s \n", output.String())
	if err := cmd.Wait(); err != nil {
		//print out docker-compose sysout & syserr for error diagnosis
		fmt.Printf(output.String())
		DeleteTempFile(dockerComposeFile)
		return &DockerError{errOpDockerComposeStart, err, err.Error()}
	}
	fmt.Printf(output.String()) // Wait to finish execution, so we can read all output

	if strings.Contains(output.String(), "ERROR") || strings.Contains(output.String(), "error") {
		DeleteTempFile(dockerComposeFile)
		os.Exit(1)
	}

	if strings.Contains(output.String(), "The image for the service you're trying to recreate has been removed") {
		DeleteTempFile(dockerComposeFile)
		os.Exit(1)
	}
	return nil
}

// DockerComposeStop to stop Codewind containers
func DockerComposeStop(tag, dockerComposeFile string) *DockerError {
	setupDockerComposeEnvs(tag, "stop", "")
	cmd := exec.Command("docker-compose", "-f", dockerComposeFile, "rm", "--stop", "-f")
	output := new(bytes.Buffer)
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Start(); err != nil { // after 'Start' the program is continued and script is executing in background
		return &DockerError{errOpDockerComposeStop, err, err.Error()}
	}
	fmt.Printf("Please wait while containers shutdown... %s \n", output.String())
	if err := cmd.Wait(); err != nil {
		return &DockerError{errOpDockerComposeStop, err, err.Error()}
	}
	fmt.Printf(output.String()) // Wait to finish execution, so we can read all output

	if strings.Contains(output.String(), "ERROR") || strings.Contains(output.String(), "error") {
		os.Exit(1)
	}
	return nil
}

// DockerComposeRemove to remove Codewind images
func DockerComposeRemove(dockerComposeFile, tag string) *DockerError {
	setupDockerComposeEnvs(tag, "remove", "")
	cmd := exec.Command("docker-compose", "-f", dockerComposeFile, "down", "--rmi", "all")
	output := new(bytes.Buffer)
	cmd.Stdout = output
	cmd.Stderr = output
	// after 'Start' the program is continued and script is executing in background
	err := cmd.Start()
	if err != nil {
		return &DockerError{errOpDockerComposeRemove, err, err.Error()}
	}
	fmt.Printf("Please wait whilst images are removed... %s \n", output.String())
	err = cmd.Wait()
	if err != nil {
		return &DockerError{errOpImageRemove, err, err.Error()}
	}
	fmt.Printf(output.String()) // Wait to finish execution, so we can read all output

	if strings.Contains(output.String(), "ERROR") || strings.Contains(output.String(), "error") {
		os.Exit(1)
	}
	return nil
}

// setupDockerComposeEnvs for docker-compose to use
func setupDockerComposeEnvs(tag, command string, loglevel string) {
	home := os.Getenv("HOME")
	os.Setenv("PFE_IMAGE_NAME", pfeImageName)
	os.Setenv("PERFORMANCE_IMAGE_NAME", performanceImageName)

	const GOARCH string = runtime.GOARCH
	const GOOS string = runtime.GOOS
	fmt.Println("System architecture is: ", GOARCH)
	fmt.Println("Host operating system is: ", GOOS)
	if GOARCH == "x86_64" || GOARCH == "amd64" {
		os.Setenv("PLATFORM", "-amd64")
	} else {
		os.Setenv("PLATFORM", "-"+GOARCH)
	}

	os.Setenv("TAG", tag)
	if GOOS == "windows" {
		os.Setenv("WORKSPACE_DIRECTORY", "C:\\codewind-data")
		// In Windows, calling the env variable "HOME" does not return
		// the user directory correctly
		os.Setenv("HOST_HOME", os.Getenv("USERPROFILE"))
	} else {
		os.Setenv("WORKSPACE_DIRECTORY", home+"/codewind-data")
		os.Setenv("HOST_HOME", home)
	}
	os.Setenv("HOST_OS", GOOS)
	os.Setenv("COMPOSE_PROJECT_NAME", "codewind")
	os.Setenv("HOST_MAVEN_OPTS", os.Getenv("MAVEN_OPTS"))

	if command == "remove" || command == "stop" {
		os.Setenv("PFE_EXTERNAL_PORT", "")
	} else {
		fmt.Printf("Attempting to find available port\n")
		portAvailable, port := IsTCPPortAvailable(minTCPPort, maxTCPPort)
		if !portAvailable {
			fmt.Printf("No available external ports in range, will default to Docker-assigned port")
		}
		os.Setenv("PFE_EXTERNAL_PORT", port)
	}
	os.Setenv("LOG_LEVEL", loglevel)
}

// PullImage - pull pfe/performance images from dockerhub
func PullImage(image string, jsonOutput bool) *DockerError {
	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.WithVersion("1.30"))
	if err != nil {
		return &DockerError{errOpImageNotFound, err, err.Error()}
	}

	var codewindOut io.ReadCloser

	codewindOut, err = cli.ImagePull(ctx, image, types.ImagePullOptions{})

	if err != nil {
		return &DockerError{errOpImagePull, err, err.Error()}
	}

	if jsonOutput == true {
		defer codewindOut.Close()
		io.Copy(os.Stdout, codewindOut)
	} else {
		defer codewindOut.Close()
		termFd, isTerm := term.GetFdInfo(os.Stderr)
		jsonmessage.DisplayJSONMessagesStream(codewindOut, os.Stderr, termFd, isTerm, nil)
	}
	return nil
}

// ValidateImageDigest - will ensure the image digest matches that of the one in dockerhub
// returns imageID, docker error
func ValidateImageDigest(image string) (string, *DockerError) {

	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.WithVersion("1.30"))
	if err != nil {
		return "", &DockerError{errOpImageDigest, err, err.Error()}
	}
	// call docker api for image digest
	queryDigest, err := cli.DistributionInspect(ctx, image, "")
	if err != nil {
		logr.Error(err)
	}

	// turn digest -> []byte -> string
	digest, _ := json.Marshal(queryDigest.Descriptor.Digest)
	logr.Traceln("Query image digest is.. ", queryDigest.Descriptor.Digest)

	// get local image digest
	imageList, dockerError := GetImageList()
	if err != nil {
		return "", dockerError
	}

	imageName := strings.TrimPrefix(image, "docker.io/")
	imageArr := []string{
		imageName,
	}

	for _, image := range imageList {
		imageRepo := strings.Join(image.RepoDigests, " ")
		imageTags := strings.Join(image.RepoTags, " ")
		for _, index := range imageArr {
			if strings.Contains(imageTags, index) {
				if strings.Contains(imageRepo, strings.Replace(string(digest), "\"", "", -1)) {
					length := len(strings.Replace(string(digest), "\"", "", -1))
					last10 := strings.Replace(string(digest), "\"", "", -1)[length-10 : length]
					logr.Tracef("Validation for image digest ..%v succeeded\n", last10)
				} else {
					logr.Traceln("Local image digest did not match queried image digest from dockerhub - This could be a result of a bad download")
					valError := goErr.New(textBadDigest)
					return image.ID, &DockerError{errOpValidate, valError, valError.Error()}
				}
			}
		}
	}
	return "", nil
}

// TagImage - locally retag the downloaded images
func TagImage(source, tag string) *DockerError {
	out, err := exec.Command("docker", "tag", source, tag).Output()
	if err != nil {
		return &DockerError{errOpImageTag, err, err.Error()}
	}

	output := string(out[:])
	fmt.Println(output)
	return nil
}

// GetContainersToRemove returns a list of containers ([]types.Container) matching "/cw"
func GetContainersToRemove(containerList []types.Container) []types.Container {
	codewindContainerPrefixes := []string{
		"/cw-",
	}

	containersToRemove := []types.Container{}
	for _, container := range containerList {
		for _, prefix := range codewindContainerPrefixes {
			if strings.HasPrefix(container.Names[0], prefix) {
				containersToRemove = append(containersToRemove, container)
				break
			}
		}
	}
	return containersToRemove
}

// CheckContainerStatus of Codewind running/stopped
func CheckContainerStatus() (bool, *DockerError) {
	var containerStatus = false
	containerArr := containerImageNames
	containers, err := GetContainerList()
	if err != nil {
		return false, err
	}

	containerCount := 0
	for _, container := range containers {
		for _, key := range containerArr {
			if len(container.Names) != 1 {
				continue
			}
			// The container names returned by docker are prefixed with "/"
			if strings.HasPrefix(container.Names[0], "/"+key) {
				containerCount++
			}
		}
	}
	if containerCount >= 2 {
		containerStatus = true
	} else {
		containerStatus = false
	}
	return containerStatus, nil
}

// CheckImageStatus of Codewind installed/uninstalled
func CheckImageStatus() (bool, *DockerError) {
	var imageStatus = false
	imageArr := baseImageNameArr
	images, err := GetImageList()
	if err != nil {
		return false, err
	}

	imageCount := 0
	for _, image := range images {
		imageRepo := strings.Join(image.RepoDigests, " ")
		for _, key := range imageArr {
			if strings.HasPrefix(imageRepo, key) {
				imageCount++
			}
		}
	}
	if imageCount >= 2 {
		imageStatus = true
	}
	return imageStatus, nil
}

// RemoveImage of Codewind and project
func RemoveImage(imageID string) *DockerError {
	cmd := exec.Command("docker", "rmi", imageID, "-f")
	cmd.Stdin = strings.NewReader("some input")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		return &DockerError{errOpImageRemove, err, err.Error()}
	}
	return nil
}

// GetContainerList from docker
func GetContainerList() ([]types.Container, *DockerError) {
	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.WithVersion("1.30"))
	if err != nil {
		return nil, &DockerError{errOpContainerList, err, err.Error()}
	}

	containers, err := cli.ContainerList(ctx, types.ContainerListOptions{})
	if err != nil {
		return nil, &DockerError{errOpContainerList, err, err.Error()}
	}
	return containers, nil
}

// GetImageList from docker
func GetImageList() ([]types.ImageSummary, *DockerError) {
	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.WithVersion("1.30"))
	if err != nil {
		return nil, &DockerError{errOpImageList, err, err.Error()}
	}

	images, err := cli.ImageList(ctx, types.ImageListOptions{})
	if err != nil {
		return nil, &DockerError{errOpImageList, err, err.Error()}
	}
	return images, nil
}

// StopContainer will stop only codewind containers
func StopContainer(container types.Container) *DockerError {
	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.WithVersion("1.30"))
	if err != nil {
		return &DockerError{errOpStopContainer, err, err.Error()}
	}

	// Check if the container will remove after it is stopped
	isAutoRemoved, isAutoRemovedErr := getContainerAutoRemovePolicy(container.ID)
	if isAutoRemovedErr != nil {
		return &DockerError{errOpStopContainer, err, err.Error()}
	}

	// Stop the running container
	err = cli.ContainerStop(ctx, container.ID, nil)
	if err != nil {
		return &DockerError{errOpStopContainer, err, err.Error()}
	}

	if !isAutoRemoved {
		// Remove the container so it isnt lingering in the background
		err = cli.ContainerRemove(ctx, container.ID, types.ContainerRemoveOptions{})
		if err != nil {
			return &DockerError{errOpStopContainer, err, err.Error()}
		}
	}
	return nil
}

// getContainerAutoRemovePolicy will get the auto remove policy of a given container
func getContainerAutoRemovePolicy(containerID string) (bool, *DockerError) {
	ctx := context.Background()

	cli, err := client.NewClientWithOpts(client.WithVersion("1.30"))
	if err != nil {
		return false, &DockerError{errOpClientCreate, err, err.Error()}
	}

	containerInfo, err := cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return false, &DockerError{errOpContainerInspect, err, err.Error()}
	}

	return containerInfo.HostConfig.AutoRemove, nil
}

// GetPFEHostAndPort will return the current hostname and port that PFE is running on
func GetPFEHostAndPort() (string, string, *DockerError) {
	containerIsRunning, err := CheckContainerStatus()
	if err != nil {
		return "", "", err
	}
	// on Che, can assume PFE is always on localhost:9090
	if os.Getenv("CHE_API_EXTERNAL") != "" {
		return "localhost", "9090", nil
	} else if containerIsRunning {
		containerList, err := GetContainerList()
		if err != nil {
			return "", "", err
		}
		for _, container := range containerList {
			if strings.HasPrefix(container.Image, pfeImageName) {
				for _, port := range container.Ports {
					if port.PrivatePort == internalPFEPort {
						return port.IP, strconv.Itoa(int(port.PublicPort)), nil
					}
				}
			}
		}
	}
	return "", "", nil
}

// GetImageTags of Codewind images
func GetImageTags() ([]string, *DockerError) {
	imageArr := baseImageNameArr
	tagArr := []string{}

	images, err := GetImageList()
	if err != nil {
		return nil, err
	}

	for _, image := range images {
		imageRepo := strings.Join(image.RepoDigests, " ")
		imageTags := strings.Join(image.RepoTags, " ")
		for _, key := range imageArr {
			if strings.HasPrefix(imageRepo, key) || strings.HasPrefix(imageTags, key) {
				if len(image.RepoTags) > 0 {
					tag := image.RepoTags[0]
					tag = strings.Split(tag, ":")[1]
					tagArr = append(tagArr, tag)
				} else {
					logr.Debug("No tag available for '" + imageRepo + "'. Defaulting to ''")
					tagArr = append(tagArr, "")
				}
			}
		}
	}

	tagArr = RemoveDuplicateEntries(tagArr)
	return tagArr, err
}

// IsTCPPortAvailable checks to find the next available port and returns it
func IsTCPPortAvailable(minTCPPort int, maxTCPPort int) (bool, string) {
	var status string
	for port := minTCPPort; port < maxTCPPort; port++ {
		conn, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
		if err != nil {
			log.Println("Unable to connect to port", port, ":", err)
		} else {
			status = "Port " + strconv.Itoa(port) + " Available"
			fmt.Println(status)
			conn.Close()
			return true, strconv.Itoa(port)
		}
	}
	return false, ""
}

// DetermineDebugPortForPFE determines a debug port to use for PFE based on the external PFE port
func DetermineDebugPortForPFE() (pfeDebugPort string) {
	_, debugPort := IsTCPPortAvailable(minDebugPort, maxDebugPort)
	return debugPort
}

// GetContainerTags of the Codewind version(s) currently running
func GetContainerTags() ([]string, *DockerError) {
	containerArr := baseImageNameArr
	tagArr := []string{}

	containers, err := GetContainerList()
	if err != nil {
		return nil, err
	}

	for _, container := range containers {
		for _, key := range containerArr {
			if strings.HasPrefix(container.Image, key) {
				tag := strings.Split(container.Image, ":")[1]
				tagArr = append(tagArr, tag)
			}
		}
	}

	tagArr = RemoveDuplicateEntries(tagArr)
	return tagArr, nil
}
