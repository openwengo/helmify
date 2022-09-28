package statefulset

import (
	"testing"

	"github.com/arttor/helmify/pkg/metadata"

	"github.com/arttor/helmify/internal"
	"github.com/stretchr/testify/assert"
)

const (
	  strStatefl = `apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: redis
spec:
  serviceName: "redis"
  selector:
    matchLabels:
      app: redis
  updateStrategy:
    type: RollingUpdate
  replicas: 3
  template:
    metadata:
      labels:
        app: redis
    spec:
      containers:
      - name: redis
        image: redis
        resources:
          limits:
            memory: 2Gi
        ports:
          - containerPort: 6379
        volumeMounts:
          - name: redis-data
            mountPath: /usr/share/redis
  volumeClaimTemplates:
  - metadata:
      name: redis-data
    spec:
      accessModes: [ "ReadWriteOnce" ]
      resources:
        requests:
          storage: 10Gi`
)

func Test_statefulset_Process(t *testing.T) {
	var testInstance statefulset

	t.Run("processed", func(t *testing.T) {
		obj := internal.GenerateObj(strStatefl)
		processed, _, err := testInstance.Process(&metadata.Service{}, obj)
		assert.NoError(t, err)
		assert.Equal(t, true, processed)
	})
	t.Run("skipped", func(t *testing.T) {
		obj := internal.TestNs
		processed, _, err := testInstance.Process(&metadata.Service{}, obj)
		assert.NoError(t, err)
		assert.Equal(t, false, processed)
	})
}
