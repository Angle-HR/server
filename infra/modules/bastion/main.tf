# SSM-only bastion for reaching private resources (RDS) from a laptop: no public IP, no inbound rules, no SSH key.
# Access is an SSM port-forwarding session (see infra/scripts/db-tunnel.sh), authorised by the caller's IAM identity.
variable "name" { type = string }
variable "vpc_id" { type = string }
variable "subnet_id" { type = string }
variable "instance_type" {
  type    = string
  default = "t4g.nano"
}

# Standard (not minimal) AL2023: it ships the SSM agent.
data "aws_ssm_parameter" "al2023_arm64" {
  name = "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-arm64"
}

data "aws_iam_policy_document" "assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "this" {
  name               = "${var.name}-bastion"
  assume_role_policy = data.aws_iam_policy_document.assume.json
}

resource "aws_iam_role_policy_attachment" "ssm" {
  role       = aws_iam_role.this.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

resource "aws_iam_instance_profile" "this" {
  name = "${var.name}-bastion"
  role = aws_iam_role.this.name
}

resource "aws_security_group" "this" {
  name        = "${var.name}-bastion"
  description = "SSM bastion: no inbound, egress for SSM and the forwarded connections"
  vpc_id      = var.vpc_id
}

resource "aws_vpc_security_group_egress_rule" "all" {
  security_group_id = aws_security_group.this.id
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "-1"
}

resource "aws_instance" "this" {
  ami                         = data.aws_ssm_parameter.al2023_arm64.value
  instance_type               = var.instance_type
  subnet_id                   = var.subnet_id
  vpc_security_group_ids      = [aws_security_group.this.id]
  iam_instance_profile        = aws_iam_instance_profile.this.name
  associate_public_ip_address = false
  metadata_options {
    http_tokens = "required"
  }
  root_block_device {
    encrypted = true
  }
  tags = { Name = "${var.name}-bastion" }
  lifecycle {
    ignore_changes = [ami] # a newer AL2023 release should not replace a running bastion
  }
}

output "instance_id" { value = aws_instance.this.id }
output "security_group_id" { value = aws_security_group.this.id }
