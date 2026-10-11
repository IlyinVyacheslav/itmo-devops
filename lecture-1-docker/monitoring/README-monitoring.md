1) Установка чарта
```bash
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update
helm upgrade --install monitoring prometheus-community/kube-prometheus-stack \
  -n monitoring --create-namespace -f monitoring-values.yaml
```
2) Установка cAdvisor
```bash
kubectl apply -k https://github.com/google/cadvisor//deploy/kubernetes/base
```
3) Файл cadvicor-servicemonitor.yaml чтобы Prometheus автоматически обнаружил cAdvisor
```bash
kubectl apply -f cadvisor-servicemonitor.yaml
```
4) Запуск Prometheus (http://localhost:9090 → Status → Targets → cadvisor должен быть UP)
```bash
kubectl -n monitoring port-forward svc/monitoring-kube-prometheus-prometheus 9090:9090
```
5) Дашборд в Grafana - в файле api-dashboard.yaml JSON дашборда с панелями: память, CPU throttling, процессы и сеть
```bash
kubectl apply -f api-dashboard.yaml
```
6) Алерты
```bash
kubectl apply -f api-rules.yaml
```
7) Запуск, получение пароля админа
```bash
kubectl -n monitoring get secret monitoring-grafana -o jsonpath='{.data.admin-password}' | base64 -d #получение пароля админа
kubectl -n monitoring port-forward svc/monitoring-grafana 3000:80
kubectl -n monitoring port-forward svc/monitoring-kube-prometheus-prometheus 9090:9090 # проверяем в Alerts
```
Запуск api:
```bash
docker run --rm -d --name=api \
  --memory=100m --memory-swap=100m \
  --cpus=0.5 --pids-limit=15 \
  --cap-drop=ALL --security-opt=no-new-privileges \
  -p 8081:8080 \
  api:scratch
```
Нужно вызвать /burn + /eat?mb=150 - на дашборде будет рост CPU, throttling + приближение к лимиту памяти, в Prometheus
алерты перейдут через pending в firing.