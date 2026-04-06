package cmd

import (
	"testing"
)

const testPurgeECSClusterName = "my-cluster"

// --- iamToCloudControl: user and group branches ---

func TestIAMToCloudControlUser(t *testing.T) {
	cfnType, id := iamToCloudControl("iam/user", "my-user", "arn:aws:iam::123:user/my-user")
	if cfnType != "AWS::IAM::User" || id != "my-user" {
		t.Fatalf("iamToCloudControl(iam/user) = (%q, %q)", cfnType, id)
	}
}

func TestIAMToCloudControlGroup(t *testing.T) {
	cfnType, id := iamToCloudControl("iam/group", "my-group", "arn:aws:iam::123:group/my-group")
	if cfnType != "AWS::IAM::Group" || id != "my-group" {
		t.Fatalf("iamToCloudControl(iam/group) = (%q, %q)", cfnType, id)
	}
}

// --- ec2ToCloudControl: route-table, subnet, security-group ---

func TestEC2ToCloudControlRouteTable(t *testing.T) {
	cfnType, id := ec2ToCloudControl("ec2/route-table", "rtb-123")
	if cfnType != "AWS::EC2::RouteTable" || id != "rtb-123" {
		t.Fatalf("ec2ToCloudControl(ec2/route-table) = (%q, %q)", cfnType, id)
	}
}

func TestEC2ToCloudControlSubnet(t *testing.T) {
	cfnType, id := ec2ToCloudControl("ec2/subnet", "subnet-abc")
	if cfnType != "AWS::EC2::Subnet" || id != "subnet-abc" {
		t.Fatalf("ec2ToCloudControl(ec2/subnet) = (%q, %q)", cfnType, id)
	}
}

func TestEC2ToCloudControlSecurityGroup(t *testing.T) {
	cfnType, id := ec2ToCloudControl("ec2/security-group", "sg-xyz")
	if cfnType != "AWS::EC2::SecurityGroup" || id != "sg-xyz" {
		t.Fatalf("ec2ToCloudControl(ec2/security-group) = (%q, %q)", cfnType, id)
	}
}

func TestEC2ToCloudControlInternetGateway(t *testing.T) {
	cfnType, id := ec2ToCloudControl("ec2/internet-gateway", "igw-001")
	if cfnType != "AWS::EC2::InternetGateway" || id != "igw-001" {
		t.Fatalf("ec2ToCloudControl(ec2/internet-gateway) = (%q, %q)", cfnType, id)
	}
}

// --- ecsToCloudControl: cluster, service ---

func TestECSToCloudControlCluster(t *testing.T) {
	cfnType, id := ecsToCloudControl("ecs/cluster", testPurgeECSClusterName, "arn:aws:ecs:us-east-1:123:cluster/"+testPurgeECSClusterName)
	if cfnType != "AWS::ECS::Cluster" || id != testPurgeECSClusterName {
		t.Fatalf("ecsToCloudControl(ecs/cluster) = (%q, %q)", cfnType, id)
	}
}

func TestECSToCloudControlService(t *testing.T) {
	arn := "arn:aws:ecs:us-east-1:123:service/my-cluster/my-service"
	cfnType, id := ecsToCloudControl("ecs/service", "my-service", arn)
	if cfnType != "AWS::ECS::Service" || id != arn {
		t.Fatalf("ecsToCloudControl(ecs/service) = (%q, %q)", cfnType, id)
	}
}

// --- lightsailToCloudControl: Instance, Disk, Bucket ---

func TestLightsailToCloudControlInstance(t *testing.T) {
	cfnType, id := lightsailToCloudControl("lightsail/Instance", "my-instance")
	if cfnType != "AWS::Lightsail::Instance" || id != "my-instance" {
		t.Fatalf("lightsailToCloudControl(lightsail/Instance) = (%q, %q)", cfnType, id)
	}
}

func TestLightsailToCloudControlDisk(t *testing.T) {
	cfnType, id := lightsailToCloudControl("lightsail/Disk", "my-disk")
	if cfnType != "AWS::Lightsail::Disk" || id != "my-disk" {
		t.Fatalf("lightsailToCloudControl(lightsail/Disk) = (%q, %q)", cfnType, id)
	}
}

func TestLightsailToCloudControlBucket(t *testing.T) {
	cfnType, id := lightsailToCloudControl("lightsail/Bucket", "my-bucket")
	if cfnType != "AWS::Lightsail::Bucket" || id != "my-bucket" {
		t.Fatalf("lightsailToCloudControl(lightsail/Bucket) = (%q, %q)", cfnType, id)
	}
}

func TestLightsailToCloudControlKeyPair(t *testing.T) {
	cfnType, id := lightsailToCloudControl("lightsail/KeyPair", "my-key")
	if cfnType != "AWS::Lightsail::KeyPair" || id != "my-key" {
		t.Fatalf("lightsailToCloudControl(lightsail/KeyPair) = (%q, %q)", cfnType, id)
	}
}

// --- arnToCloudControl: extended branches ---

func TestArnToCloudControlSNS(t *testing.T) {
	arn := "arn:aws:sns:us-east-1:123:my-topic"
	cfnType, id := arnToCloudControl(arn, "sns", "sns", "my-topic")
	if cfnType != "AWS::SNS::Topic" || id != arn {
		t.Fatalf("arnToCloudControl(sns) = (%q, %q)", cfnType, id)
	}
}

func TestArnToCloudControlSQS(t *testing.T) {
	arn := "arn:aws:sqs:us-east-1:123:my-queue"
	cfnType, id := arnToCloudControl(arn, "sqs", "sqs", "my-queue")
	if cfnType != "AWS::SQS::Queue" || id != arn {
		t.Fatalf("arnToCloudControl(sqs) = (%q, %q)", cfnType, id)
	}
}

func TestArnToCloudControlLambdaFunction(t *testing.T) {
	arn := "arn:aws:lambda:us-east-1:123:function:my-fn"
	cfnType, id := arnToCloudControl(arn, "lambda", "lambda/function", "my-fn")
	if cfnType != "AWS::Lambda::Function" || id != "my-fn" {
		t.Fatalf("arnToCloudControl(lambda/function) = (%q, %q)", cfnType, id)
	}
}

func TestArnToCloudControlLambdaTypeMismatch(t *testing.T) {
	cfnType, id := arnToCloudControl("arn", "lambda", "lambda/layer", "my-layer")
	if cfnType != "" || id != "" {
		t.Fatalf("arnToCloudControl(lambda/layer) should return empty, got (%q, %q)", cfnType, id)
	}
}

func TestArnToCloudControlECRRepository(t *testing.T) {
	arn := "arn:aws:ecr:us-east-1:123:repository/my-repo"
	cfnType, id := arnToCloudControl(arn, "ecr", "ecr/repository", "my-repo")
	if cfnType != "AWS::ECR::Repository" || id != "my-repo" {
		t.Fatalf("arnToCloudControl(ecr/repository) = (%q, %q)", cfnType, id)
	}
}

func TestArnToCloudControlLogsLogGroup(t *testing.T) {
	arn := "arn:aws:logs:us-east-1:123:log-group:/aws/lambda/fn"
	cfnType, id := arnToCloudControl(arn, "logs", "logs/log-group", "/aws/lambda/fn")
	if cfnType != "AWS::Logs::LogGroup" || id != "/aws/lambda/fn" {
		t.Fatalf("arnToCloudControl(logs/log-group) = (%q, %q)", cfnType, id)
	}
}

func TestArnToCloudControlSecretsManagerSecret(t *testing.T) {
	arn := "arn:aws:secretsmanager:us-east-1:123:secret:my-secret"
	cfnType, id := arnToCloudControl(arn, "secretsmanager", "secretsmanager/secret", "my-secret")
	if cfnType != "AWS::SecretsManager::Secret" || id != arn {
		t.Fatalf("arnToCloudControl(secretsmanager/secret) = (%q, %q)", cfnType, id)
	}
}

func TestArnToCloudControlSSMParameter(t *testing.T) {
	arn := "arn:aws:ssm:us-east-1:123:parameter/my-param"
	cfnType, id := arnToCloudControl(arn, "ssm", "ssm/parameter", "/my-param")
	if cfnType != "AWS::SSM::Parameter" || id != "/my-param" {
		t.Fatalf("arnToCloudControl(ssm/parameter) = (%q, %q)", cfnType, id)
	}
}

func TestArnToCloudControlKMSKey(t *testing.T) {
	arn := "arn:aws:kms:us-east-1:123:key/key-id"
	cfnType, id := arnToCloudControl(arn, "kms", "kms/key", "key-id")
	if cfnType != "AWS::KMS::Key" || id != "key-id" {
		t.Fatalf("arnToCloudControl(kms/key) = (%q, %q)", cfnType, id)
	}
}

func TestArnToCloudControlELBLoadBalancer(t *testing.T) {
	arn := "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/my-lb/abc"
	cfnType, id := arnToCloudControl(arn, "elasticloadbalancing", "elasticloadbalancing/loadbalancer", "my-lb")
	if cfnType != "AWS::ElasticLoadBalancingV2::LoadBalancer" || id != arn {
		t.Fatalf("arnToCloudControl(elasticloadbalancing/loadbalancer) = (%q, %q)", cfnType, id)
	}
}

func TestArnToCloudControlECSClusterViaArn(t *testing.T) {
	arn := "arn:aws:ecs:us-east-1:123:cluster/" + testPurgeECSClusterName
	cfnType, id := arnToCloudControl(arn, "ecs", "ecs/cluster", testPurgeECSClusterName)
	if cfnType != "AWS::ECS::Cluster" || id != testPurgeECSClusterName {
		t.Fatalf("arnToCloudControl(ecs/cluster) = (%q, %q)", cfnType, id)
	}
}

func TestArnToCloudControlEventsRule(t *testing.T) {
	arn := "arn:aws:events:us-east-1:123:rule/my-rule"
	cfnType, id := arnToCloudControl(arn, "events", "events/rule", "my-rule")
	if cfnType != "AWS::Events::Rule" || id != "my-rule" {
		t.Fatalf("arnToCloudControl(events/rule) = (%q, %q)", cfnType, id)
	}
}

func TestArnToCloudControlSageMakerNotebookInstance(t *testing.T) {
	arn := "arn:aws:sagemaker:us-east-1:123:notebook-instance/my-nb"
	cfnType, id := arnToCloudControl(arn, "sagemaker", "sagemaker/notebook-instance", "my-nb")
	if cfnType != "AWS::SageMaker::NotebookInstance" || id != "my-nb" {
		t.Fatalf("arnToCloudControl(sagemaker/notebook-instance) = (%q, %q)", cfnType, id)
	}
}

func TestArnToCloudControlServiceDiscoveryNamespace(t *testing.T) {
	arn := "arn:aws:servicediscovery:us-east-1:123:namespace/ns-id"
	cfnType, id := arnToCloudControl(arn, "servicediscovery", "servicediscovery/namespace", "ns-id")
	if cfnType != "AWS::ServiceDiscovery::PrivateDnsNamespace" || id != "ns-id" {
		t.Fatalf("arnToCloudControl(servicediscovery/namespace) = (%q, %q)", cfnType, id)
	}
}

func TestArnToCloudControlLightsailInstanceViaArn(t *testing.T) {
	arn := "arn:aws:lightsail:us-east-1:123:Instance/my-inst"
	cfnType, id := arnToCloudControl(arn, "lightsail", "lightsail/Instance", "my-inst")
	if cfnType != "AWS::Lightsail::Instance" || id != "my-inst" {
		t.Fatalf("arnToCloudControl(lightsail/Instance) = (%q, %q)", cfnType, id)
	}
}

func TestArnToCloudControlDynamoDBTypeMismatch(t *testing.T) {
	cfnType, id := arnToCloudControl("arn", "dynamodb", "dynamodb/stream", "stream-name")
	if cfnType != "" || id != "" {
		t.Fatalf("arnToCloudControl(dynamodb/stream) should return empty, got (%q, %q)", cfnType, id)
	}
}
