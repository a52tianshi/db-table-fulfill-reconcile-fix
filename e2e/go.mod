module github.com/a52tianshi/db-table-fulfill-reconcile-fix/e2e

go 1.22

require (
	github.com/a52tianshi/db-table-fulfill-reconcile-fix v0.0.0
	github.com/go-sql-driver/mysql v1.8.1
)

require filippo.io/edwards25519 v1.1.0 // indirect

// The framework under test is the parent directory; keeps the root module dependency-free.
replace github.com/a52tianshi/db-table-fulfill-reconcile-fix => ../
