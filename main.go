// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX - License - Identifier: Apache - 2.0
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// CWEnableAlarmAPI defines the interface for the PutMetricAlarm function.
// We use this interface to test the function using a mocked service.
type CWEnableAlarmAPI interface {
	PutMetricAlarm(ctx context.Context,
		params *cloudwatch.PutMetricAlarmInput,
		optFns ...func(*cloudwatch.Options)) (*cloudwatch.PutMetricAlarmOutput, error)
	EnableAlarmActions(ctx context.Context,
		params *cloudwatch.EnableAlarmActionsInput,
		optFns ...func(*cloudwatch.Options)) (*cloudwatch.EnableAlarmActionsOutput, error)
}

var (
	instanceName    string
	tagKey          string
	instanceIDs     string
	alarmNamePrefix string
	running         bool
	snsTopic        string // Default_CloudWatch_Alarms_Topic
	action          string // Terminate, Stop, Reboot
	threshold       float64   // 0-100
	period          int 	 // 1, 5, 10, 30, or multiples of 60
)

// CreateMetricAlarm creates a metric alarm
// Inputs:
//     c is the context of the method call, which includes the Region
//     api is the interface that defines the method call
//     input defines the input arguments to the service call.
// Output:
//     If success, a PutMetricAlarmOutput object containing the result of the service call and nil
//     Otherwise, the error from a call to PutMetricAlarm
func CreateMetricAlarm(c context.Context, api CWEnableAlarmAPI, input *cloudwatch.PutMetricAlarmInput) (*cloudwatch.PutMetricAlarmOutput, error) {
	return api.PutMetricAlarm(c, input)
}

// EnableAlarm enables the specified Amazon CloudWatch alarm
// Inputs:
//     c is the context of the method call, which includes the Region
//     api is the interface that defines the method call
//     input defines the input arguments to the service call.
// Output:
//     If success, a EnableAlarmActionsOutput object containing the result of the service call and nil
//     Otherwise, the error from a call to PutMetricAlarm
func EnableAlarm(c context.Context, api CWEnableAlarmAPI, input *cloudwatch.EnableAlarmActionsInput) (*cloudwatch.EnableAlarmActionsOutput, error) {
	return api.EnableAlarmActions(c, input)
}

// GetInstanceIds returns a list of instance IDs
func GetInstanceIds(cfg aws.Config, name string, tagKey string, ids []string, running bool) []string {
	client := ec2.NewFromConfig(cfg)

	var filterName, filterTag, filterStatus ec2types.Filter
	if name != "" {
		tag1 := "tag:Name"
		filterName = ec2types.Filter{
			Name:   &tag1,
			Values: []string{name},
		}
	}

	if tagKey != "" {
		tag2 := "tag-key"
		filterTag = ec2types.Filter{
			Name:   &tag2,
			Values: []string{tagKey},
		}
	}

	if running {
		tag3 := "instance-state-name"
		filterStatus = ec2types.Filter{
			Name:   &tag3,
			Values: []string{"running"},
		}
	}

	var (
		outputs *ec2.DescribeInstancesOutput
		err     error
	)
	if ids[0] == "" {
		outputs, err = client.DescribeInstances(context.TODO(), &ec2.DescribeInstancesInput{Filters: []ec2types.Filter{filterName, filterTag, filterStatus}})
	} else {
		outputs, err = client.DescribeInstances(context.TODO(), &ec2.DescribeInstancesInput{Filters: []ec2types.Filter{filterName, filterTag, filterStatus}, InstanceIds: ids})
	}
	if err != nil {
		log.Fatal(err)
	}

	instacneIds := make([]string, 0)
	for _, reservation := range outputs.Reservations {
		for _, instance := range reservation.Instances {
			fmt.Printf("%s (%s): %v\n", *instance.InstanceId, instance.InstanceType, instance.State.Name)
			instacneIds = append(instacneIds, *instance.InstanceId)
		}
	}
	return instacneIds
}

func CreatePerInstance(cfg aws.Config, instanceID, account *string) {

	client := cloudwatch.NewFromConfig(cfg)

	alarmName := fmt.Sprintf("awsec2-%s-%s", *instanceID, alarmNamePrefix)
	putInput := &cloudwatch.PutMetricAlarmInput{
		AlarmName:          &alarmName,
		ComparisonOperator: types.ComparisonOperatorLessThanThreshold,
		EvaluationPeriods:  aws.Int32(1),
		MetricName:         aws.String("CPUUtilization"),
		Namespace:          aws.String("AWS/EC2"),
		Period:             aws.Int32(900),
		Statistic:          types.StatisticAverage,
		Threshold:          aws.Float64(threshold),
		ActionsEnabled:     aws.Bool(true),
		AlarmDescription:   aws.String(fmt.Sprintf("Alarm when server CPU falls below %f percent", threshold)),
		AlarmActions: []string{
			fmt.Sprintf("arn:aws:swf:"+cfg.Region+":%s:action/actions/AWS_EC2.InstanceId.%s/1.0", *account, action),
			fmt.Sprintf("arn:aws:sns:"+cfg.Region+":%s:%s", *account, snsTopic),
		},
		Dimensions: []types.Dimension{
			{
				Name:  aws.String("InstanceId"),
				Value: instanceID,
			},
		},
	}

	_, err := CreateMetricAlarm(context.TODO(), client, putInput)
	if err != nil {
		fmt.Println(err)
		return
	}

	enableInput := &cloudwatch.EnableAlarmActionsInput{
		AlarmNames: []string{
			alarmName,
		},
	}

	_, err = EnableAlarm(context.TODO(), client, enableInput)
	if err != nil {
		fmt.Println(err)
		return
	}

	fmt.Println("Enabled alarm " + alarmName + " for EC2 instance " + *instanceID)
}

func init() {
	flag.StringVar(&instanceName, "n", "", "Name of EC2 instances")
	flag.StringVar(&tagKey, "t", "", "Tag key of EC2 instances")
	flag.StringVar(&instanceIDs, "i", "", "EC2 instance IDs: e.g. i-1234567890abcdef0,i-1234567890abcdef1")
	flag.StringVar(&alarmNamePrefix, "a", "", "Alarm name prefix")
	flag.StringVar(&snsTopic, "s", "", "a SNS topic to notify")
	flag.BoolVar(&running, "r", false, "Create alarms only for running instances")
	flag.StringVar(&action, "action", "Terminate", "EC2 action to take when alarm is triggered: Terminate, Stop, or Reboot (default: Terminate))")
	flag.Float64Var(&threshold, "thres", 1.0, "CPU Utilization threshold to trigger alarm (default: 1.0)")
	flag.IntVar(&period, "p", 900, "Period in seconds (default: 900)")
}

func errhandler(dryrun bool) {
	if dryrun {
		log.Println("Dry run, Skip error handling")
		return
	}
	if instanceName == "" && tagKey == "" && instanceIDs == "" {
		log.Fatalln("You must provide an instance name, a tag key, or instance IDs")
	}
	if alarmNamePrefix == "" {
		log.Fatalln("You must provide an alarm name prefix")
	}
	if snsTopic == "" {
		log.Fatalln("You must provide a SNS topic")
	}
	if action != "Terminate" && action != "Stop" && action != "Reboot" {
		log.Fatalln("Valid actions are Terminate, Stop, or Reboot")
	}
	if period != 1 && period != 5 && period != 10 && period != 30 && period % 60 != 0 {
		log.Fatalln("Valid periods are 1, 5, 10, 30, or multiples of 60")
	}
}

func main() {
	flag.Parse()
	errhandler(false)

	ids_slice := strings.Split(instanceIDs, ",")

	cfg, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		panic("configuration error, " + err.Error())
	}

	ids := GetInstanceIds(cfg, instanceName, tagKey, ids_slice, running)

	stssvc := sts.NewFromConfig(cfg)
	input := &sts.GetCallerIdentityInput{}
	output, err := stssvc.GetCallerIdentity(context.TODO(), input)
	if err != nil {
		log.Fatalln("sts error: " + err.Error())
	}

	for _, id := range ids {
		CreatePerInstance(cfg, &id, output.Account)
	}

}
