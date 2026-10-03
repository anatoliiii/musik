package db

// Compatibility aliases keep the repository transaction signatures stable
// while SQLite and PostgreSQL share the GORM-backed Database adapter.
type Connection = Database
type Tx = DatabaseTx
