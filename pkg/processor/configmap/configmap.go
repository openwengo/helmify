package configmap

import (
	"fmt"
	"github.com/arttor/helmify/pkg/processor"
	"io"
	"strings"

	"github.com/arttor/helmify/pkg/helmify"
	yamlformat "github.com/arttor/helmify/pkg/yaml"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"
)

var configMapGVC = schema.GroupVersionKind{
	Group:   "",
	Version: "v1",
	Kind:    "ConfigMap",
}

// New creates processor for k8s ConfigMap resource.
func New() helmify.Processor {
	return &configMap{}
}

type configMap struct{}

// Process k8s ConfigMap object into template. Returns false if not capable of processing given resource type.
func (d configMap) Process(info helmify.ChartInfo, obj *unstructured.Unstructured) (bool, helmify.Template, error) {
	if obj.GroupVersionKind() != configMapGVC {
		return false, nil, nil
	}
	name, template, err := processor.ProcessMetadata(info, obj)
	if err != nil {
		return true, nil, err
	}

	if immutable, exists, _ := unstructured.NestedBool(obj.Object, "immutable"); exists {
		immutableStr, err := yamlformat.Marshal(map[string]interface{}{"immutable": immutable}, 0)
		if err != nil {
			return true, nil, err
		}
		template += immutableStr + "\n"
	}
	if binaryData, exists, _ := unstructured.NestedStringMap(obj.Object, "binaryData"); exists {
		binaryDataStr, err := yamlformat.Marshal(map[string]interface{}{"binaryData": binaryData}, 0)
		if err != nil {
			return true, nil, err
		}
		template += binaryDataStr + "\n"
	}

	var values helmify.Values
	if data, exists, _ := unstructured.NestedStringMap(obj.Object, "data"); exists {
		data, values = parseMapData(data, name)
		dataStr, err := yamlformat.Marshal(map[string]interface{}{"data": data}, 0)
		if err != nil {
			return true, nil, err
		}
		dataStr = strings.ReplaceAll(dataStr, "'", "")
		template += dataStr
	}

	return true, &result{
		name:   name + ".yaml",
		data:   []byte(template),
		values: values,
	}, nil
}

func parseMapData(data map[string]string, configName string) (map[string]string, helmify.Values) {
	values := helmify.Values{}
	for key, value := range data {
		valuesNamePath := []string{configName, key}
		if strings.HasSuffix(key, ".yaml") || strings.HasSuffix(key, ".yml") {
			templated, err := parseYaml(value, valuesNamePath, values)
			if err != nil {
				logrus.WithError(err).Errorf("unable to process configmap data: %v", valuesNamePath)
				continue
			}
			data[key] = templated
			continue
		}
		if strings.HasSuffix(key, ".properties") {
			templated, err := parseProperties(value, valuesNamePath, values)
			if err != nil {
				logrus.WithError(err).Errorf("unable to process configmap data: %v", valuesNamePath)
				continue
			}
			data[key] = templated
			continue
		}
		templatedVal, err := values.Add(value, valuesNamePath)
		if err != nil {
			logrus.WithError(err).Errorf("unable to process configmap data: %v", valuesNamePath)
			continue
		}
		data[key] = templatedVal
	}
	return data, values
}

func parseYaml(value string, path []string, values helmify.Values) (string, error) {
	config := map[string]interface{}{}
	err := yaml.Unmarshal([]byte(value), &config)
	if err != nil {
		return "", errors.Wrapf(err, "unable to unmarshal configmap %v", path)
	}
	parseConfig(config, values, path)
	confBytes, err := yaml.Marshal(config)
	if err != nil {
		return "", errors.Wrapf(err, "unable to marshal configmap %v", path)
	}
	return string(confBytes), nil
}

func parseProperties(properties string, path []string, values helmify.Values) (string, error) {
	var res strings.Builder
	for _, line := range strings.Split(strings.TrimSuffix(properties, "\n"), "\n") {
		prop := strings.Split(line, "=")
		if len(prop) != 2 {
			return "", errors.Errorf("wrong property format in %v: %s", path, line)
		}
		propName, propVal := prop[0], prop[1]
		propNamePath := strings.Split(propName, ".")
		templatedVal, err := values.Add(propVal, append(path, propNamePath...))
		if err != nil {
			return "", err
		}
		_, err = res.WriteString(propName + "=" + templatedVal + "\n")
		if err != nil {
			return "", errors.Wrap(err, "unable to write to string builder")
		}
	}
	return res.String(), nil
}

func parseConfig(config map[string]interface{}, values helmify.Values, path []string) {
	for k, v := range config {
		switch t := v.(type) {
		case string, bool, float64, int64:
			if k == "kind" || k == "apiVersion" {
				continue
			}
			templated, err := values.Add(v, append(path, k))
			if err != nil {
				logrus.WithError(err).Error()
				continue
			}
			config[k] = templated
		case []interface{}:
			logrus.Warn("configmap: arrays not supported")
		case map[string]interface{}:
			parseConfig(t, values, append(path, k))
		case map[interface{}]interface{}:
			c, ok := v.(map[string]interface{})
			if !ok {
				logrus.Warn("configmap: unable to cast to map[string]interface{}")
				continue
			}
			parseConfig(c, values, append(path, k))
		default:
			logrus.Warn("configmap: unknown type ", t)
			fmt.Printf("\n%T\n", t)
		}
	}
}

type result struct {
	name   string
	data   []byte
	values helmify.Values
}

func (r *result) Filename() string {
	return r.name
}

func (r *result) Values() helmify.Values {
	return r.values
}

func (r *result) Write(writer io.Writer) error {
	_, err := writer.Write(r.data)
	return err
}
