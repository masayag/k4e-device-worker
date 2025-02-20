package workload

import (
	"fmt"
	"git.sr.ht/~spc/go-log"
	api2 "github.com/jakub-dzon/k4e-device-worker/internal/workload/api"
	podman2 "github.com/jakub-dzon/k4e-device-worker/internal/workload/podman"
	"github.com/jakub-dzon/k4e-operator/models"
	"io/ioutil"
	v1 "k8s.io/api/core/v1"
	"os"
	"path"
	"sigs.k8s.io/yaml"
	"strings"
	"time"
)

type WorkloadManager struct {
	manifestsDir string
	workloads    api2.WorkloadAPI
}

func NewWorkloadManager(configDir string) (*WorkloadManager, error) {
	manifestsDir := path.Join(configDir, "manifests")
	if err := os.MkdirAll(manifestsDir, 0755); err != nil {
		return nil, fmt.Errorf("cannot create directory: %w", err)
	}
	newPodman, err := podman2.NewPodman()
	if err != nil {
		return nil, err
	}

	manager := WorkloadManager{
		manifestsDir: manifestsDir,
		workloads:    newPodman,
	}
	go func() {
		for {
			err := manager.ensureWorkloadsFromManifestsAreRunning()
			if err != nil {
				log.Error(err)
			}
			time.Sleep(time.Second * 15)
		}
	}()

	return &manager, nil
}

func (w *WorkloadManager) ListWorkloads() ([]api2.WorkloadInfo, error) {
	return w.workloads.List()
}

func (w *WorkloadManager) Update(configuration models.DeviceConfigurationMessage) error {
	workloads := configuration.Workloads
	if len(workloads) == 0 {
		log.Trace("No workloads")

		// Purge all the workloads
		err := w.purgeWorkloads()
		if err != nil {
			return err
		}
		// Remove manifests
		err = w.removeManifests()
		if err != nil {
			return err
		}
		return nil
	}

	for _, workload := range workloads {
		podName := workload.Name
		log.Tracef("Deploying workload: %s", podName)
		// TODO: change error handling from fail fast to best effort (deploy as many workloads as possible)
		manifestPath, err := w.storeManifest(workload)
		if err != nil {
			return err
		}

		err = w.workloads.Remove(podName)
		if err != nil {
			log.Errorf("Error removing workload: %v", err)
			return err
		}
		err = w.workloads.Run(manifestPath)
		if err != nil {
			log.Errorf("Cannot run workload: %v", err)
			return err
		}
	}
	return nil
}

func (w *WorkloadManager) purgeWorkloads() error {
	podList, err := w.workloads.List()
	if err != nil {
		log.Errorf("Cannot list workloads: %v", err)
		return err
	}
	for _, podReport := range podList {
		err := w.workloads.Remove(podReport.Name)
		if err != nil {
			log.Errorf("Error removing workload: %v", err)
			return err
		}
	}
	return nil
}

func (w *WorkloadManager) removeManifests() error {
	manifestInfo, err := ioutil.ReadDir(w.manifestsDir)
	if err != nil {
		return err
	}
	for _, fi := range manifestInfo {
		filePath := path.Join(w.manifestsDir, fi.Name())
		err := os.Remove(filePath)
		if err != nil {
			return err
		}
	}
	return nil
}

func (w *WorkloadManager) storeManifest(workload *models.Workload) (string, error) {
	podYaml, err := w.toPodYaml(workload)
	if err != nil {
		return "", err
	}
	fileName := strings.ReplaceAll(workload.Name, " ", "-") + ".yaml"
	filePath := path.Join(w.manifestsDir, fileName)
	err = ioutil.WriteFile(filePath, podYaml, 0640)
	if err != nil {
		return "", err
	}
	return filePath, nil
}

func (w *WorkloadManager) ensureWorkloadsFromManifestsAreRunning() error {
	manifestInfo, err := ioutil.ReadDir(w.manifestsDir)
	if err != nil {
		return err
	}
	workloads, err := w.workloads.List()
	if err != nil {
		return err
	}
	nameToWorkload := make(map[string]api2.WorkloadInfo)
	for _, workload := range workloads {
		nameToWorkload[workload.Name] = workload
	}
	for _, fi := range manifestInfo {
		filePath := path.Join(w.manifestsDir, fi.Name())
		manifest, err := ioutil.ReadFile(filePath)
		if err != nil {
			log.Error(err)
			continue
		}
		pod := v1.Pod{}
		err = yaml.Unmarshal(manifest, &pod)
		if err != nil {
			log.Error(err)
			continue
		}
		if workload, ok := nameToWorkload[pod.Name]; ok {
			if workload.Status != "Running" {
				// Workload is not running - start
				err = w.workloads.Start(pod.Name)
				if err != nil {
					log.Error(err)
				}
			}
			continue
		}
		// Workload is not present - run
		err = w.workloads.Run(filePath)
		if err != nil {
			log.Error(err)
			continue
		}
	}
	return nil
}

func (w *WorkloadManager) toPodYaml(workload *models.Workload) ([]byte, error) {
	podSpec := v1.PodSpec{}

	err := yaml.Unmarshal([]byte(workload.Specification), &podSpec)
	if err != nil {
		return nil, err
	}
	pod := v1.Pod{
		Spec: podSpec,
	}
	pod.Kind = "Pod"
	pod.Name = workload.Name
	return yaml.Marshal(pod)
}
