# AWS: run the agent as an ECS Fargate task inside the VPC that holds your
# private databases (for example RDS). The task only needs outbound access.
#
#   terraform init && terraform apply \
#     -var dsui_url=https://dsui.example.com \
#     -var 'subnet_ids=["subnet-aaa","subnet-bbb"]' \
#     -var vpc_id=vpc-123 \
#     -var 'targets=["mydb.cluster-xyz.eu-west-1.rds.amazonaws.com:5432"]'
#
# Afterwards allow the printed security group on your database's security group
# (inbound, database port) so the agent can connect.

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.0"
    }
  }
}

variable "dsui_url" { type = string }
variable "vpc_id" { type = string }
variable "subnet_ids" { type = list(string) }
variable "targets" { type = list(string) }
variable "agent_name" {
  type    = string
  default = "aws-vpc"
}
variable "image" {
  type    = string
  default = "ghcr.io/northgraindata/dsui-agent:latest"
}
variable "token_secret_arn" {
  description = "Secrets Manager secret whose value is the agent token created in DSUI."
  type        = string
}

resource "aws_ecs_cluster" "this" {
  name = "dsui-agent"
}

resource "aws_security_group" "agent" {
  name_prefix = "dsui-agent-"
  vpc_id      = var.vpc_id

  # Outbound only: DSUI over 443 and the private targets.
  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "aws_cloudwatch_log_group" "agent" {
  name              = "/dsui-agent"
  retention_in_days = 14
}

data "aws_iam_policy_document" "assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "execution" {
  name_prefix        = "dsui-agent-exec-"
  assume_role_policy = data.aws_iam_policy_document.assume.json
}

resource "aws_iam_role_policy_attachment" "execution" {
  role       = aws_iam_role.execution.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

resource "aws_iam_role_policy" "read_token" {
  role = aws_iam_role.execution.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["secretsmanager:GetSecretValue"]
      Resource = [var.token_secret_arn]
    }]
  })
}

data "aws_region" "current" {}

resource "aws_ecs_task_definition" "agent" {
  family                   = "dsui-agent"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = 256
  memory                   = 512
  execution_role_arn       = aws_iam_role.execution.arn

  container_definitions = jsonencode([{
    name      = "dsui-agent"
    image     = var.image
    essential = true
    command   = ["run"]
    readonlyRootFilesystem = true
    environment = [
      { name = "DSUI_URL", value = var.dsui_url },
      { name = "DSUI_AGENT_NAME", value = var.agent_name },
      { name = "DSUI_TARGETS", value = join(",", var.targets) },
      { name = "DSUI_LOG_FORMAT", value = "json" },
    ]
    secrets = [{ name = "DSUI_TOKEN", valueFrom = var.token_secret_arn }]
    logConfiguration = {
      logDriver = "awslogs"
      options = {
        awslogs-group         = aws_cloudwatch_log_group.agent.name
        awslogs-region        = data.aws_region.current.name
        awslogs-stream-prefix = "agent"
      }
    }
  }])
}

resource "aws_ecs_service" "agent" {
  name            = "dsui-agent"
  cluster         = aws_ecs_cluster.this.id
  task_definition = aws_ecs_task_definition.agent.arn
  desired_count   = 1 # one agent per site
  launch_type     = "FARGATE"

  network_configuration {
    subnets          = var.subnet_ids
    security_groups  = [aws_security_group.agent.id]
    assign_public_ip = false # use a NAT gateway for the outbound connection to DSUI
  }
}

output "agent_security_group_id" {
  description = "Allow this security group on your database's inbound rules."
  value       = aws_security_group.agent.id
}
