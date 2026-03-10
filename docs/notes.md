# notes

Krun is a command-line tool for building, deploying, and debugging microservices in Kubernetes environments.
Specifically designed to work with my msa-templates (See: https://github.com/ftechmax/msa-templates)

The tool can list all available services by scaning the git dir and check for projects that have a krun.json file which descibes the project parts and build/deploy setup.

It can also build the solution by deploying a build pod in your k8s cluster, sync the source over and build in the pod. It then pushes the image to the local registry inside the cluster.

Then it can deploy/delete the k8s stack using the image built in the previous step and the k8s kustomize manifests in the projects k8s folder.

The final (and biggest) part of the cli tool is the ability to set up a debug session to the app running in the cluster. It consists of:
- local elevated helper daemon `krun-helper`
- a k8s deployed service called `traffic-manager`
- an injected k8s sidecar called `traffic-agent`

All parts are written in Go v1.25

The elevated helper daemon running on the dev computer primarily manages the hosts file and local port-forwards for service dependencies listed in `krun.json` (`service_dependencies`). For example `[ { "host": "rabbitmq.default.svc", "port": 5672 } ]`. When a debug session is started the helper writes dependency host mappings to the local hosts file, opens dependency forwards to `127.0.0.1:<port>`, and tracks session state. If the helper is closed it closes the forwards and cleans up the hosts file. Application traffic interception is routed through traffic-agent -> traffic-manager -> helper -> local app (not by a direct target-service port-forward to the local intercept port).

The traffic manager is a simple controller that runs in the development kubernetes cluster. The krun cli tool can open a port forward to this service and issue rest calls to /v1/sessions to start, stop and list debug sessions.
When a session is created the controller will inject a traffic-agent container in the target application pod. This agent will then use iptables to intercept the traffic of the targeted container and redirect it to the traffic-manager. When a session is stopped the traffic-manager will remove the agent container. 
