from alembic import context


connection = context.config.attributes.get("connection")
if connection is None:
    raise RuntimeError("musik migrations must be started through `musik db migrate`")

context.configure(
    connection=connection,
    target_metadata=None,
    compare_type=True,
    render_as_batch=connection.dialect.name == "sqlite",
)

with context.begin_transaction():
    context.run_migrations()
