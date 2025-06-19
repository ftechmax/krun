package build

import "github.com/voortman-steel-machinery/krun/internal/utils"

func createBuildPod(kubeConfig string) {
	manifest := `
apiVersion: v1
kind: PersistentVolume
metadata:
  labels:
    type: local
  name: docker-build-git-pv
spec:
  accessModes:
  - ReadWriteOnce
  capacity:
    storage: 20Gi
  claimRef:
    name: docker-build-git-pvc
    namespace: default
  hostPath:
    path: /var/workspace
  storageClassName: ""
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: docker-build-git-pvc
  namespace: default
spec:
  accessModes:
  - ReadWriteOnce
  resources:
    requests:
      storage: 20Gi
  storageClassName: ""
---
apiVersion: v1
kind: Pod
metadata:
  labels:
    app: docker-build
  name: docker-build
  namespace: default
spec:
  containers:
  - args:
    - --insecure-registry=registry:5000
    image: docker:dind
    name: docker-build
    securityContext:
      privileged: true
    volumeMounts:
    - mountPath: /workspace
      name: docker-build-context
    - mountPath: /var/lib/docker
      name: docker-build-lib
  volumes:
  - name: docker-build-context
    persistentVolumeClaim:
      claimName: docker-build-git-pvc
  - emptyDir: {}
    name: docker-build-lib
`
	err := utils.RunCmdStdin(manifest, "kubectl", "--kubeconfig="+kubeConfig, "apply", "-f", "-")
	if err != nil {
		panic("Failed to create build pod: " + err.Error())
	}

	err = utils.RunCmd("kubectl", "--kubeconfig="+kubeConfig, "wait", "--for", "condition=Ready", "--timeout=90s", "pod", "-l", "app=docker-build")
	if err != nil {
		panic("Failed to wait for build pod to be ready: " + err.Error())
	}
}