package build

import (
	"crypto/rand"
	"encoding/base64"
	"strings"

	"github.com/ftechmax/krun/internal/utils"
)

const manifest = `
apiVersion: v1
kind: ConfigMap
metadata:
  name: docker-build-registries
  namespace: default
data:
  registries.conf: |
    [registries.search]
    registries = ['docker.io']

    [registries.insecure]
    registries = ['registry:5000']
---
apiVersion: v1
kind: Pod
metadata:
  labels:
    app: docker-build
  name: docker-build
  namespace: default
spec:
  securityContext:
    fsGroup: 1001
  containers:
  - image: quay.io/buildah/stable:latest
    name: docker-build
    securityContext:
      privileged: true
      runAsGroup: 1001
    command: ["sleep", "infinity"]
    volumeMounts:
      - mountPath: /var/workspace
        name: workspace
      - mountPath: /etc/containers/registries.conf
        name: docker-build-registries
        subPath: registries.conf
  - name: sftp
    image: atmoz/sftp:alpine
    args: ["user:##password##:1001:1001:workspace"]
    securityContext:
      privileged: false
      runAsGroup: 1001
    ports:
      - containerPort: 22
    volumeMounts:
      - mountPath: /home/user/workspace
        name: workspace
  volumes:
  - name: docker-build-lib
    emptyDir: {}
  - name: workspace
    emptyDir: {}
  - name: docker-build-registries
    configMap:
      name: docker-build-registries
      items:
        - key: registries.conf
          path: registries.conf
---
apiVersion: v1
kind: Service
metadata:
  name: docker-build
  namespace: default
spec:
  type: NodePort
  selector:
    app: docker-build
  ports:
    - protocol: TCP
      port: 22
      targetPort: 22
`

func createBuildPod(kubeConfig string) (int32, string, error) {

	password, err := generatePassword(16)
	if err != nil {
		panic("Failed to generate password: " + err.Error())
	}
	manifest := strings.ReplaceAll(manifest, "##password##", password)

	err = utils.RunCmdStdin(manifest, "kubectl", "--kubeconfig="+kubeConfig, "apply", "-f", "-")
	if err != nil {
		panic("Failed to create build pod: " + err.Error())
	}

	err = utils.RunCmd("kubectl", "--kubeconfig="+kubeConfig, "wait", "--for", "condition=Ready", "--timeout=90s", "pod", "-l", "app=docker-build")
	if err != nil {
		panic("Failed to wait for build pod to be ready: " + err.Error())
	}

	nodePort, _ := utils.GetServiceNodePort(kubeConfig, "default", "docker-build", 22)
	return nodePort, password, nil
}

func generatePassword(length int) (string, error) {
	b := make([]byte, length)
	_, err := rand.Read(b)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b)[:length], nil
}
