resource "random_password" "db" {
  length  = 32
  special = false
}

resource "aws_db_subnet_group" "billing" {
  name       = local.name
  subnet_ids = local.private_subnet_ids
}

resource "aws_security_group" "db" {
  name        = "${local.name}-db"
  description = "PostgreSQL do billing-service"
  vpc_id      = local.vpc_id
}

resource "aws_vpc_security_group_ingress_rule" "db_from_nodes" {
  security_group_id            = aws_security_group.db.id
  referenced_security_group_id = local.node_security_group
  ip_protocol                  = "tcp"
  from_port                    = 5432
  to_port                      = 5432
}

resource "aws_db_instance" "billing" {
  identifier              = local.name
  engine                  = "postgres"
  engine_version          = "17"
  instance_class          = var.db_instance_class
  allocated_storage       = var.db_allocated_storage
  storage_encrypted       = true
  db_name                 = "billing"
  username                = "billing"
  password                = random_password.db.result
  db_subnet_group_name    = aws_db_subnet_group.billing.name
  vpc_security_group_ids  = [aws_security_group.db.id]
  publicly_accessible     = false
  skip_final_snapshot     = true
  deletion_protection     = false
  backup_retention_period = 1
}

resource "aws_secretsmanager_secret" "billing" {
  name                    = "oficina-mecanica/${var.environment}/billing-service"
  recovery_window_in_days = 0
}

resource "aws_secretsmanager_secret_version" "billing" {
  secret_id = aws_secretsmanager_secret.billing.id
  secret_string = jsonencode({
    DATABASE_PASSWORD          = random_password.db.result
    JWT_SECRET_KEY             = "CHANGE_ME"
    MERCADOPAGO_ACCESS_TOKEN   = "CHANGE_ME"
    MERCADOPAGO_WEBHOOK_SECRET = "CHANGE_ME"
    SES_SENDER_EMAIL           = "CHANGE_ME"
  })

  lifecycle {
    ignore_changes = [secret_string]
  }
}
