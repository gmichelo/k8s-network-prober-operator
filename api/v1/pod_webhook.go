/*
Copyright 2022.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	probesv1alpha1 "github.com/bigmikes/k8s-network-prober-operator/api/v1alpha1"
	"github.com/go-logr/logr"
)

const (
	AnnotationKey = "netprober-container-deployed"
	AnnotationVal = "true"
)

// SidecarInjecter is a mutating admission controller that
// injects the network-prober sidecar container to
// Pods that match the selector in NetworkProber CRD object.
type SidecarInjecter struct {
	Client  client.Client
	decoder *admission.Decoder
}

//+kubebuilder:webhook:path=/mutate-v1-pod,mutating=true,failurePolicy=fail,groups="",resources=pods,verbs=create,versions=v1,name=mpod.bigmikes.io,sideEffects=None,webhookVersions=v1,admissionReviewVersions=v1

var _ admission.DecoderInjector = &SidecarInjecter{}

// InjectDecoder injects the decoder.
func (a *SidecarInjecter) InjectDecoder(d *admission.Decoder) error {
	a.decoder = d
	return nil
}

// Handle intercepts Pod API requests and injects the sidecar container
func (a *SidecarInjecter) Handle(ctx context.Context, req admission.Request) admission.Response {
	log := log.FromContext(ctx)

	pod := &corev1.Pod{}

	err := a.decoder.Decode(req, pod)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	log.Info("Mutating", "name", pod.Name)

	netProberList := &probesv1alpha1.NetworkProberList{}
	err = a.Client.List(ctx, netProberList)
	if err != nil {
		log.Error(err, "failed to get list of NetworkProber")
		return admission.Errored(http.StatusInternalServerError, err)
	}
	for _, netProber := range netProberList.Items {
		log.Info("Evaluating selector", "net-prober", netProber.Name)
		selector := labels.SelectorFromSet(netProber.Spec.PodSelector.MatchLabels)
		if selector.Matches(labels.Set(pod.Labels)) {
			log.Info("Selector matched", "net-prober", netProber.Name)
			if pod.Annotations == nil {
				pod.Annotations = map[string]string{}
			}
			if val := pod.Annotations[AnnotationKey]; val != AnnotationVal {
				err := injectSidecar(log, pod, netProber)
				if err != nil {
					log.Error(err, "failed to inject container in Pod")
					return admission.Errored(http.StatusInternalServerError, err)
				}
			}
		}
	}

	marshaledPod, err := json.Marshal(pod)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}

	return admission.PatchResponseFromRaw(req.Object.Raw, marshaledPod)
}

func injectSidecar(log logr.Logger, pod *corev1.Pod, netProber probesv1alpha1.NetworkProber) error {
	pingPort, err := strconv.Atoi(netProber.Spec.HttpPort)
	if err != nil {
		log.Error(err, "failed to convert port string to int")
		return err
	}
	prometheusPort, err := strconv.Atoi(netProber.Spec.HttpPrometheusPort)
	if err != nil {
		log.Error(err, "failed to convert port string to int")
		return err
	}
	pod.Annotations[AnnotationKey] = AnnotationVal
	pod.Spec.Containers = append(pod.Spec.Containers, corev1.Container{
		Name:  "net-prober",
		Image: netProber.Spec.AgentImage,
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "net-prober-vol",
				MountPath: "/etc/netprober",
			},
		},
		Env: []corev1.EnvVar{
			{Name: "HTTP_PORT", Value: netProber.Spec.HttpPort},
			{Name: "HTTP_PROMETHEUS_PORT", Value: netProber.Spec.HttpPrometheusPort},
		},
		Ports: []corev1.ContainerPort{
			{Name: "np-ping", ContainerPort: int32(pingPort)},
			{Name: "np-prometheus", ContainerPort: int32(prometheusPort)},
		},
	})
	volumeMode := int32(420)
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name: "net-prober-vol",
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				DefaultMode: &volumeMode,
				LocalObjectReference: corev1.LocalObjectReference{
					Name: netProber.Name,
				},
			},
		},
	})
	return nil
}
