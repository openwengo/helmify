package statefulset

import (
	"fmt"
	"io"
	"strings"
	"text/template"

	"github.com/arttor/helmify/pkg/cluster"
	"github.com/arttor/helmify/pkg/processor"

	"github.com/arttor/helmify/pkg/helmify"
	yamlformat "github.com/arttor/helmify/pkg/yaml"
	"github.com/iancoleman/strcase"
	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var statefulsetGVC = schema.GroupVersionKind{
	Group:   "apps",
	Version: "v1",
	Kind:    "StatefulSet",
}

var statefulsetTempl, _ = template.New("statefulset").Parse(
	`{{- .Meta }}
spec:
{{- if .Replicas }}
{{ .Replicas }}
{{- end }}
  selector:
{{ .Selector }}
  template:
    metadata:
      labels:
{{ .PodLabels }}
{{- .PodAnnotations }}
    spec:
{{ .Spec }}
{{- if .VolumeClaimTemplates }}
{{ .VolumeClaimTemplates }}
{{- end }}`)

const selectorTempl = `%[1]s
{{- include "%[2]s.selectorLabels" . | nindent 6 }}
%[3]s`

// New creates processor for k8s Statefulset resource.
func New() helmify.Processor {
	return &statefulset{}
}

type statefulset struct{}

// Process k8s Statefulset object into template. Returns false if not capable of processing given resource type.
func (d statefulset) Process(appMeta helmify.AppMetadata, obj *unstructured.Unstructured) (bool, helmify.Template, error) {
	if obj.GroupVersionKind() != statefulsetGVC {
		return false, nil, nil
	}
	statefl := appsv1.StatefulSet{}
	err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &statefl)
	if err != nil {
		return true, nil, errors.Wrap(err, "unable to cast to statefulset")
	}
	meta, err := processor.ProcessObjMeta(appMeta, obj)
	if err != nil {
		return true, nil, err
	}

	values := helmify.Values{}

	name := appMeta.TrimName(obj.GetName())
	replicas, err := processReplicas(name, &statefl, &values)
	if err != nil {
		return true, nil, err
	}

	matchLabels, err := yamlformat.Marshal(map[string]interface{}{"matchLabels": statefl.Spec.Selector.MatchLabels}, 0)
	if err != nil {
		return true, nil, err
	}
	matchExpr := ""
	if statefl.Spec.Selector.MatchExpressions != nil {
		matchExpr, err = yamlformat.Marshal(map[string]interface{}{"matchExpressions": statefl.Spec.Selector.MatchExpressions}, 0)
		if err != nil {
			return true, nil, err
		}
	}
	selector := fmt.Sprintf(selectorTempl, matchLabels, appMeta.ChartName(), matchExpr)
	selector = strings.Trim(selector, " \n")
	selector = string(yamlformat.Indent([]byte(selector), 4))

	podLabels, err := yamlformat.Marshal(statefl.Spec.Template.ObjectMeta.Labels, 8)
	if err != nil {
		return true, nil, err
	}
	podLabels += fmt.Sprintf("\n      {{- include \"%s.selectorLabels\" . | nindent 8 }}", appMeta.ChartName())

	podAnnotations := ""
	if len(statefl.Spec.Template.ObjectMeta.Annotations) != 0 {
		podAnnotations, err = yamlformat.Marshal(map[string]interface{}{"annotations": statefl.Spec.Template.ObjectMeta.Annotations}, 6)
		if err != nil {
			return true, nil, err
		}

		podAnnotations = "\n" + podAnnotations
	}

	nameCamel := strcase.ToLowerCamel(name)
	podValues, err := processPodSpec(nameCamel, appMeta, &statefl.Spec.Template.Spec)
	if err != nil {
		return true, nil, err
	}
	err = values.Merge(podValues)
	if err != nil {
		return true, nil, err
	}

	// replace PVC to templated name
	for i := 0; i < len(statefl.Spec.Template.Spec.Volumes); i++ {
		vol := statefl.Spec.Template.Spec.Volumes[i]
		if vol.PersistentVolumeClaim == nil {
			continue
		}
		tempPVCName := appMeta.TemplatedName(vol.PersistentVolumeClaim.ClaimName)
		statefl.Spec.Template.Spec.Volumes[i].PersistentVolumeClaim.ClaimName = tempPVCName
	}

	// replace container resources with template to values.
	specMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&statefl.Spec.Template.Spec)
	if err != nil {
		return true, nil, err
	}
	containers, _, err := unstructured.NestedSlice(specMap, "containers")
	if err != nil {
		return true, nil, err
	}
	for i := range containers {
		containerName := strcase.ToLowerCamel((containers[i].(map[string]interface{})["name"]).(string))
		res, exists, err := unstructured.NestedMap(values, nameCamel, containerName, "resources")
		if err != nil {
			return true, nil, err
		}
		if !exists || len(res) == 0 {
			continue
		}
		err = unstructured.SetNestedField(containers[i].(map[string]interface{}), fmt.Sprintf(`{{- toYaml .Values.%s.%s.resources | nindent 10 }}`, nameCamel, containerName), "resources")
		if err != nil {
			return true, nil, err
		}
	}
	err = unstructured.SetNestedSlice(specMap, containers, "containers")
	if err != nil {
		return true, nil, err
	}


	spec, err := yamlformat.Marshal(specMap, 6)
	if err != nil {
		return true, nil, err
	}
	spec = strings.ReplaceAll(spec, "'", "")

	//VolumeClaimTemplates

	volumeClaimTemplates := ""
	if len(statefl.Spec.VolumeClaimTemplates) != 0 {
		volumeClaimTemplates, err = yamlformat.Marshal(map[string]interface{}{"VolumeClaimTemplates": statefl.Spec.VolumeClaimTemplates}, 2)
		if err != nil {
			return true, nil, err
		}

		volumeClaimTemplates = "\n" + volumeClaimTemplates
	}
	//volumeClaimTemplates = strings.ReplaceAll(spec, "'", "")


	/*
	for i := 0; i < len(statefl.Spec.VolumeClaimTemplates); i++ {
		volClaim := statefl.Spec.VolumeClaimTemplates[i]
		if vol.PersistentVolumeClaim == nil {
			continue
		}
		tempPVCName := appMeta.TemplatedName(vol.PersistentVolumeClaim.ClaimName)
		statefl.Spec.Template.Spec.Volumes[i].PersistentVolumeClaim.ClaimName = tempPVCName
	}
	*/
	return true, &result{
		values: values,
		data: struct {
			Meta           string
			Replicas       string
			Selector       string
			PodLabels      string
			PodAnnotations string			
			Spec           string
			VolumeClaimTemplates string
		}{
			Meta:           meta,
			Replicas:       replicas,
			Selector:       selector,
			PodLabels:      podLabels,
			PodAnnotations: podAnnotations,
			Spec:           spec,
			VolumeClaimTemplates: volumeClaimTemplates,
		},
	}, nil
}

func processReplicas(name string, statefulset *appsv1.StatefulSet, values *helmify.Values) (string, error) {
	if statefulset.Spec.Replicas == nil {
		return "", nil
	}
	replicasTpl, err := values.Add(int64(*statefulset.Spec.Replicas), name, "replicas")
	if err != nil {
		return "", err
	}
	replicas, err := yamlformat.Marshal(map[string]interface{}{"replicas": replicasTpl}, 2)
	if err != nil {
		return "", err
	}
	replicas = strings.ReplaceAll(replicas, "'", "")
	return replicas, nil
}

func processPodSpec(name string, appMeta helmify.AppMetadata, pod *corev1.PodSpec) (helmify.Values, error) {
	values := helmify.Values{}
	for i, c := range pod.Containers {
		processed, err := processPodContainer(name, appMeta, c, &values)
		if err != nil {
			return nil, err
		}
		pod.Containers[i] = processed
	}
	for _, v := range pod.Volumes {
		if v.ConfigMap != nil {
			v.ConfigMap.Name = appMeta.TemplatedName(v.ConfigMap.Name)
		}
		if v.Secret != nil {
			v.Secret.SecretName = appMeta.TemplatedName(v.Secret.SecretName)
		}
	}
	pod.ServiceAccountName = appMeta.TemplatedName(pod.ServiceAccountName)

	for i, s := range pod.ImagePullSecrets {
		pod.ImagePullSecrets[i].Name = appMeta.TemplatedName(s.Name)
	}

	return values, nil
}

func processPodContainer(name string, appMeta helmify.AppMetadata, c corev1.Container, values *helmify.Values) (corev1.Container, error) {
	index := strings.LastIndex(c.Image, ":")
	if index < 0 {
		return c, errors.New("wrong image format: " + c.Image)
	}
	repo, tag := c.Image[:index], c.Image[index+1:]
	containerName := strcase.ToLowerCamel(c.Name)
	c.Image = fmt.Sprintf("{{ .Values.%[1]s.%[2]s.image.repository }}:{{ .Values.%[1]s.%[2]s.image.tag | default .Chart.AppVersion }}", name, containerName)

	err := unstructured.SetNestedField(*values, repo, name, containerName, "image", "repository")
	if err != nil {
		return c, errors.Wrap(err, "unable to set statefulset value field")
	}
	err = unstructured.SetNestedField(*values, tag, name, containerName, "image", "tag")
	if err != nil {
		return c, errors.Wrap(err, "unable to set statefulset value field")
	}
	for _, e := range c.Env {
		if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
			e.ValueFrom.SecretKeyRef.Name = appMeta.TemplatedName(e.ValueFrom.SecretKeyRef.Name)
		}
		if e.ValueFrom != nil && e.ValueFrom.ConfigMapKeyRef != nil {
			e.ValueFrom.ConfigMapKeyRef.Name = appMeta.TemplatedName(e.ValueFrom.ConfigMapKeyRef.Name)
		}
	}
	for _, e := range c.EnvFrom {
		if e.SecretRef != nil {
			e.SecretRef.Name = appMeta.TemplatedName(e.SecretRef.Name)
		}
		if e.ConfigMapRef != nil {
			e.ConfigMapRef.Name = appMeta.TemplatedName(e.ConfigMapRef.Name)
		}
	}
	c.Env = append(c.Env, corev1.EnvVar{
		Name:  cluster.DomainEnv,
		Value: fmt.Sprintf("{{ .Values.%s }}", cluster.DomainKey),
	})
	for k, v := range c.Resources.Requests {
		err = unstructured.SetNestedField(*values, v.ToUnstructured(), name, containerName, "resources", "requests", k.String())
		if err != nil {
			return c, errors.Wrap(err, "unable to set container resources value")
		}
	}
	for k, v := range c.Resources.Limits {
		err = unstructured.SetNestedField(*values, v.ToUnstructured(), name, containerName, "resources", "limits", k.String())
		if err != nil {
			return c, errors.Wrap(err, "unable to set container resources value")
		}
	}
	return c, nil
}

type result struct {
	data struct {
		Meta           string
		Replicas       string
		Selector       string
		PodLabels      string
		PodAnnotations string
		Spec           string
		VolumeClaimTemplates string
	}
	values helmify.Values
}

func (r *result) Filename() string {
	return "statefulset.yaml"
}

func (r *result) Values() helmify.Values {
	return r.values
}

func (r *result) Write(writer io.Writer) error {
	return statefulsetTempl.Execute(writer, r.data)
}
