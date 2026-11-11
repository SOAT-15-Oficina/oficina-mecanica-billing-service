locals {
  published = {
    billing_inbox_queue_url   = aws_sqs_queue.inbox.url
    billing_inbox_queue_arn   = aws_sqs_queue.inbox.arn
    billing_events_topic_arn  = aws_sns_topic.events.arn
    billing_ecr_repository    = aws_ecr_repository.service.repository_url
    billing_database_endpoint = aws_db_instance.billing.address
    billing_database_name     = aws_db_instance.billing.db_name
    billing_database_user     = aws_db_instance.billing.username
    billing_secret_arn        = aws_secretsmanager_secret.billing.arn
    billing_role_arn          = aws_iam_role.service.arn
    billing_target_group_arn  = aws_lb_target_group.service.arn
  }
}

resource "aws_ssm_parameter" "published" {
  for_each = local.published

  name      = "${local.ssm_prefix}/${each.key}"
  type      = "String"
  value     = each.value
  overwrite = true
}

output "published" {
  value = local.published
}
