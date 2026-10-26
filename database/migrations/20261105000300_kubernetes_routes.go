package migrations

func init() {
	App.MustRegister(statements("20261105000300", "kubernetes_routes", kubernetesRoutesUp, nil))
}
