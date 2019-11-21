package main

import (
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/api/admission/v1beta1"
	"k8s.io/klog"
)


type PatchOperation struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value,omitempty"`
}


// only allow pods to pull images from specific registry.
func admitPods(ar v1beta1.AdmissionReview) *v1beta1.AdmissionResponse {
	klog.V(2).Info("admitting pods")
	podResource := metav1.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	if ar.Request.Resource != podResource {
		err := fmt.Errorf("expect resource to be %s", podResource)
		klog.Error(err)
		return toAdmissionResponse(err)
	}

	raw := ar.Request.Object.Raw
	pod := corev1.Pod{}
	deserializer := codecs.UniversalDeserializer()
	if _, _, err := deserializer.Decode(raw, nil, &pod); err != nil {
		klog.Error(err)
		return toAdmissionResponse(err)
	}
	reviewResponse := v1beta1.AdmissionResponse{}
	reviewResponse.Allowed = true

	var msg string
	fmt.Println("2.servePods ......")
	if v, ok := pod.Labels["webhook-e2e-test"]; ok {
		if v == "webhook-disallow" {
			reviewResponse.Allowed = false
			msg = msg + "the pod contains unwanted label; "
		}
		if v == "wait-forever" {
			reviewResponse.Allowed = false
			msg = msg + "the pod response should not be sent; "
			<-make(chan int) // Sleep forever - no one sends to this channel
		}
	}
	fmt.Println("3.servePods ......")
	for _, container := range pod.Spec.Containers {
		if strings.Contains(container.Name, "webhook-disallow") {
			reviewResponse.Allowed = false
			msg = msg + "the pod contains unwanted container name; "
		}
	}
	if !reviewResponse.Allowed {
		reviewResponse.Result = &metav1.Status{Message: strings.TrimSpace(msg)}
	}
	return &reviewResponse
}

func mutatePods(ar v1beta1.AdmissionReview) *v1beta1.AdmissionResponse {
	data,_ := json.Marshal(ar.Request.Object)
	fmt.Println(string(data))
	klog.V(2).Info("mutating pods")
	podResource := metav1.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	if ar.Request.Resource != podResource {
		klog.Errorf("expect resource to be %s", podResource)
		return nil
	}

	raw := ar.Request.Object.Raw
	pod := corev1.Pod{}
	deserializer := codecs.UniversalDeserializer()
	if _, _, err := deserializer.Decode(raw, nil, &pod); err != nil {
		klog.Error(err)
		return toAdmissionResponse(err)
	}
	reviewResponse := v1beta1.AdmissionResponse{}
	reviewResponse.Allowed = true
	fmt.Println("为了测试，此处指定只有pod name=demo时才注入")
	if pod.Name == "demo" {
		saMountName,saMount := getSAMount(pod.Spec.Containers)
		fmt.Println("原pod的secretMount的挂载名称："+saMountName)

		needAddedContainers := injectPod(saMount);
		patch := injectLabels()
		for _, container := range needAddedContainers{
			patch = append(patch, PatchOperation{
				Op:    "add",
				Path:  "/spec/containers/-",//此处虚注意，如果是数组类型，非第一个需要加上“/-”
				Value: container,
			})
		}
		patchBytes ,_ := json.Marshal(patch)
		reviewResponse.Patch = patchBytes
		fmt.Println("==========" + string(reviewResponse.Patch))
		pt := v1beta1.PatchTypeJSONPatch
		reviewResponse.PatchType = &pt
	}
	return &reviewResponse
}

// denySpecificAttachment denies `kubectl attach to-be-attached-pod -i -c=container1"
// or equivalent client requests.
func denySpecificAttachment(ar v1beta1.AdmissionReview) *v1beta1.AdmissionResponse {
	klog.V(2).Info("handling attaching pods")
	if ar.Request.Name != "to-be-attached-pod" {
		return &v1beta1.AdmissionResponse{Allowed: true}
	}
	podResource := metav1.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	if e, a := podResource, ar.Request.Resource; e != a {
		err := fmt.Errorf("expect resource to be %s, got %s", e, a)
		klog.Error(err)
		return toAdmissionResponse(err)
	}
	if e, a := "attach", ar.Request.SubResource; e != a {
		err := fmt.Errorf("expect subresource to be %s, got %s", e, a)
		klog.Error(err)
		return toAdmissionResponse(err)
	}

	raw := ar.Request.Object.Raw
	podAttachOptions := corev1.PodAttachOptions{}
	deserializer := codecs.UniversalDeserializer()
	if _, _, err := deserializer.Decode(raw, nil, &podAttachOptions); err != nil {
		klog.Error(err)
		return toAdmissionResponse(err)
	}
	klog.V(2).Info(fmt.Sprintf("podAttachOptions=%#v\n", podAttachOptions))
	if !podAttachOptions.Stdin || podAttachOptions.Container != "container1" {
		return &v1beta1.AdmissionResponse{Allowed: true}
	}
	return &v1beta1.AdmissionResponse{
		Allowed: false,
		Result: &metav1.Status{
			Message: "attaching to pod 'to-be-attached-pod' is not allowed",
		},
	}
}

func injectPod(secretMount corev1.VolumeMount) []corev1.Container{
	return []corev1.Container{
		{
			Name: "agent",
			Image: "prima/filebeat:6",
			ImagePullPolicy: corev1.PullIfNotPresent,
			VolumeMounts: []corev1.VolumeMount{secretMount},
		},
	}
}


//获取原容器中serviceaccount的挂载目录
func getSAMount(originContainer []corev1.Container)(string,corev1.VolumeMount){
	mountName := ""
	var mount corev1.VolumeMount
	// find service account secret volume mount(/var/run/secrets/kubernetes.io/serviceaccount,
	// https://kubernetes.io/docs/reference/access-authn-authz/service-accounts-admin/#service-account-automation) from app container
	for _, add := range originContainer {
		for _, vmount := range add.VolumeMounts {
			if vmount.MountPath == "/var/run/secrets/kubernetes.io/serviceaccount" {
				mountName = vmount.Name
				mount = vmount
			}
		}
	}
	return mountName,mount
}

func injectLabels() []PatchOperation{
	return []PatchOperation{
		{
			Op: "add",
			Path: "/metadata/labels",
			Value: map[string]string{
				"webhook-label": "HelloWebhook",
			},
		},
	}
}