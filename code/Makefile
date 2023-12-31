# Requirements: docker, kubectl, helm, ctlptl, kind, linkerd
up:
	@ctlptl apply -f infra/cluster.yaml

	@$(MAKE) -C server docker
	@docker push localhost:5005/seminar/server:latest
	@$(MAKE) -C client docker
	@docker push localhost:5005/seminar/client:latest
	# @$(MAKE) -C xdsserver docker
	# @docker push localhost:5005/seminar/xds:latest

	@helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
	@helm repo update
	@helm upgrade --install prometheus prometheus-community/kube-prometheus-stack -f infra/prometheus_stack.yaml --create-namespace --namespace monitoring
	@kubectl apply -f infra/dashboards/dashboard.yaml
	@kubectl rollout status deployment prometheus-grafana --namespace monitoring

	@kubectl --namespace monitoring port-forward deployments/prometheus-grafana 3000 2>&1 > /dev/null &

	@linkerd install --crds | kubectl apply -f -
	@linkerd install | kubectl apply -f -
	@sleep 120
	# Dashboard should now be ready!
	@echo "Grafana available at: http://localhost:3000/d/ec064806-4cba-448f-a31d-3e88ba0cc3e4/seminar?orgId=1&refresh=5s"
	@open http://localhost:3000/d/ec064806-4cba-448f-a31d-3e88ba0cc3e4/seminar?orgId=1&refresh=5s
	@start http://localhost:3000/d/ec064806-4cba-448f-a31d-3e88ba0cc3e4/seminar?orgId=1&refresh=5s
	@xdg-open http://localhost:3000/d/ec064806-4cba-448f-a31d-3e88ba0cc3e4/seminar?orgId=1&refresh=5s

	@linkerd check

	@kubectl apply -f deployments/rest/server.yaml
	@kubectl apply -f deployments/default/server.yaml
	@kubectl apply -f deployments/headless/server.yaml
	@kubectl apply -f deployments/servicemesh/server.yaml
	# @kubectl apply -f deployments/xds/server.yaml
	@kubectl rollout status deployment server --namespace rest
	@kubectl rollout status deployment server --namespace default
	@kubectl rollout status deployment server --namespace headless
	@kubectl rollout status deployment server --namespace servicemesh
	# @kubectl rollout status deployment server --namespace xds

	@kubectl apply -f deployments/rest/client.yaml
	@kubectl apply -f deployments/default/client.yaml
	@kubectl apply -f deployments/headless/client.yaml
	@kubectl apply -f deployments/servicemesh/client.yaml
	@kubectl rollout status deployment client --namespace rest
	@kubectl rollout status deployment client --namespace default
	@kubectl rollout status deployment client --namespace headless
	@kubectl rollout status deployment client --namespace servicemesh

	@sleep 300

	@kubectl scale deployment --replicas=3 server --namespace rest
	@kubectl scale deployment --replicas=3 server --namespace default
	@kubectl scale deployment --replicas=3 server --namespace headless
	@kubectl scale deployment --replicas=3 server --namespace servicemesh
	@kubectl rollout status deployment server --namespace rest
	@kubectl rollout status deployment server --namespace default
	@kubectl rollout status deployment server --namespace headless
	@kubectl rollout status deployment server --namespace servicemesh

	@sleep 300

	@kubectl rollout restart deployment client --namespace rest
	@kubectl rollout restart deployment client --namespace default
	@kubectl rollout restart deployment client --namespace headless
	@kubectl rollout restart deployment client --namespace servicemesh

	@sleep 300
	@kubectl scale deployment --replicas=30 client --namespace rest
	@kubectl scale deployment --replicas=30 client --namespace default
	@kubectl scale deployment --replicas=30 client --namespace headless
	@kubectl scale deployment --replicas=30 client --namespace servicemesh
down:
	ctlptl delete -f infra/cluster.yaml
