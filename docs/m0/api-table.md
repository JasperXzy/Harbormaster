# Official API evidence (2026-11-11)

Required means metadata required=true only; conditional requirements remain applicable. SDK method existence does not prove account permissions. No cloud requests were made.

| Product / API | Required parameters | RAM Action | SDK method |
|---|---|---|---|
| Ecs / [DescribeRegions](https://api.aliyun.com/meta/v1/products/Ecs/versions/2014-05-26/apis/DescribeRegions/api.json) | - | Not specified in public metadata | True |
| Ecs / [DescribeInstances](https://api.aliyun.com/meta/v1/products/Ecs/versions/2014-05-26/apis/DescribeInstances/api.json) | `RegionId` | `ecs:DescribeInstances` | True |
| Ecs / [DescribeInstanceTypes](https://api.aliyun.com/meta/v1/products/Ecs/versions/2014-05-26/apis/DescribeInstanceTypes/api.json) | - | Not specified in public metadata | True |
| Ecs / [DescribeNetworkInterfaces](https://api.aliyun.com/meta/v1/products/Ecs/versions/2014-05-26/apis/DescribeNetworkInterfaces/api.json) | `RegionId` | `ecs:DescribeNetworkInterfaces` | True |
| Ecs / [StartInstance](https://api.aliyun.com/meta/v1/products/Ecs/versions/2014-05-26/apis/StartInstance/api.json) | `InstanceId` | `ecs:StartInstance` | True |
| Ecs / [StopInstance](https://api.aliyun.com/meta/v1/products/Ecs/versions/2014-05-26/apis/StopInstance/api.json) | `InstanceId` | `ecs:StopInstance` | True |
| Ecs / [RebootInstance](https://api.aliyun.com/meta/v1/products/Ecs/versions/2014-05-26/apis/RebootInstance/api.json) | `InstanceId` | `ecs:RebootInstance` | True |
| Ecs / [RunInstances](https://api.aliyun.com/meta/v1/products/Ecs/versions/2014-05-26/apis/RunInstances/api.json) | `RegionId` | `ecs:RunInstances` | True |
| Ecs / [DeleteInstance](https://api.aliyun.com/meta/v1/products/Ecs/versions/2014-05-26/apis/DeleteInstance/api.json) | `InstanceId` | `ecs:DeleteInstance` | True |
| Ecs / [DescribeInstanceHistoryEvents](https://api.aliyun.com/meta/v1/products/Ecs/versions/2014-05-26/apis/DescribeInstanceHistoryEvents/api.json) | `RegionId` | `ecs:DescribeInstanceHistoryEvents` | True |
| Vpc / [DescribeEipAddresses](https://api.aliyun.com/meta/v1/products/Vpc/versions/2016-04-28/apis/DescribeEipAddresses/api.json) | `RegionId` | `vpc:DescribeEipAddresses` | True |
| Vpc / [AllocateEipAddress](https://api.aliyun.com/meta/v1/products/Vpc/versions/2016-04-28/apis/AllocateEipAddress/api.json) | `RegionId` | `vpc:AllocateEipAddress` | True |
| Vpc / [AssociateEipAddress](https://api.aliyun.com/meta/v1/products/Vpc/versions/2016-04-28/apis/AssociateEipAddress/api.json) | `AllocationId`, `InstanceId` | `vpc:AssociateEipAddress` | True |
| Vpc / [UnassociateEipAddress](https://api.aliyun.com/meta/v1/products/Vpc/versions/2016-04-28/apis/UnassociateEipAddress/api.json) | `AllocationId` | `vpc:UnassociateEipAddress` | True |
| Vpc / [ReleaseEipAddress](https://api.aliyun.com/meta/v1/products/Vpc/versions/2016-04-28/apis/ReleaseEipAddress/api.json) | `AllocationId` | `vpc:ReleaseEipAddress` | True |
| Vpc / [CreateVpc](https://api.aliyun.com/meta/v1/products/Vpc/versions/2016-04-28/apis/CreateVpc/api.json) | `RegionId` | `vpc:CreateVpc` | True |
| Vpc / [CreateVSwitch](https://api.aliyun.com/meta/v1/products/Vpc/versions/2016-04-28/apis/CreateVSwitch/api.json) | `ZoneId`, `VpcId` | `vpc:CreateVSwitch` | True |
| Vpc / [DescribeCommonBandwidthPackages](https://api.aliyun.com/meta/v1/products/Vpc/versions/2016-04-28/apis/DescribeCommonBandwidthPackages/api.json) | `RegionId` | `vpc:DescribeCommonBandwidthPackages` | True |
| SWAS-OPEN / [ListInstances](https://api.aliyun.com/meta/v1/products/SWAS-OPEN/versions/2020-06-01/apis/ListInstances/api.json) | `RegionId` | `swas-open:ListInstances` | True |
| SWAS-OPEN / [StartInstance](https://api.aliyun.com/meta/v1/products/SWAS-OPEN/versions/2020-06-01/apis/StartInstance/api.json) | `InstanceId`, `RegionId` | `swas-open:StartInstance` | True |
| SWAS-OPEN / [StopInstance](https://api.aliyun.com/meta/v1/products/SWAS-OPEN/versions/2020-06-01/apis/StopInstance/api.json) | `InstanceId`, `RegionId` | `swas-open:StopInstance` | True |
| SWAS-OPEN / [RebootInstance](https://api.aliyun.com/meta/v1/products/SWAS-OPEN/versions/2020-06-01/apis/RebootInstance/api.json) | `InstanceId`, `RegionId` | `swas-open:RebootInstance` | True |
| SWAS-OPEN / [ListInstancesTrafficPackages](https://api.aliyun.com/meta/v1/products/SWAS-OPEN/versions/2020-06-01/apis/ListInstancesTrafficPackages/api.json) | `RegionId`, `InstanceIds` | `swas-open:ListInstancesTrafficPackages` | True |
| Cms / [DescribeMetricList](https://api.aliyun.com/meta/v1/products/Cms/versions/2019-01-01/apis/DescribeMetricList/api.json) | `Namespace`, `MetricName` | `cms:QueryMetricList` | True |
| Cms / [DescribeSiteMonitorList](https://api.aliyun.com/meta/v1/products/Cms/versions/2019-01-01/apis/DescribeSiteMonitorList/api.json) | - | `cms:DescribeSiteMonitorList` | True |
| Cms / [DescribeSiteMonitorData](https://api.aliyun.com/meta/v1/products/Cms/versions/2019-01-01/apis/DescribeSiteMonitorData/api.json) | `TaskId`, `MetricName` | `cms:DescribeSiteMonitorData` | True |
| Cms / [CreateSiteMonitor](https://api.aliyun.com/meta/v1/products/Cms/versions/2019-01-01/apis/CreateSiteMonitor/api.json) | `Address`, `TaskType`, `TaskName` | `cms:CreateSiteMonitor` | True |
| BssOpenApi / [QueryAccountBalance](https://api.aliyun.com/meta/v1/products/BssOpenApi/versions/2017-12-14/apis/QueryAccountBalance/api.json) | - | `bss:DescribeAcccount` | True |
| BssOpenApi / [QueryBillOverview](https://api.aliyun.com/meta/v1/products/BssOpenApi/versions/2017-12-14/apis/QueryBillOverview/api.json) | `BillingCycle` | `bss:DescribeBillList` | True |
| BssOpenApi / [QueryInstanceBill](https://api.aliyun.com/meta/v1/products/BssOpenApi/versions/2017-12-14/apis/QueryInstanceBill/api.json) | `BillingCycle` | `bss:DescribeBillList` | True |
| Cdt / [ListCdtInternetTraffic](https://api.aliyun.com/meta/v1/products/Cdt/versions/2021-08-13/apis/ListCdtInternetTraffic/api.json) | - | Not specified in public metadata | False |

## SDK commits

- [cdt-20210813@8ee01491895e](https://github.com/alibabacloud-go/cdt-20210813/tree/8ee01491895e5288310195a9b49e147576243ce7)
- [ecs-20140526@f3703b562423](https://github.com/alibabacloud-go/ecs-20140526/tree/f3703b562423b36cb3115c54d2fa979284627e30)
- [vpc-20160428@7e9e96a54ea9](https://github.com/alibabacloud-go/vpc-20160428/tree/7e9e96a54ea924f6c793d8121afaa4cb1bb8712f)
- [swas-open-20200601@20152ee11f94](https://github.com/alibabacloud-go/swas-open-20200601/tree/20152ee11f942458506f80e08b7fd9ba5d0dc537)
- [cms-20190101@d8b98e2172d9](https://github.com/alibabacloud-go/cms-20190101/tree/d8b98e2172d922b713d4952268f91c00d50e6137)
- [bssopenapi-20171214@d1353175f379](https://github.com/alibabacloud-go/bssopenapi-20171214/tree/d1353175f37908d34008c1720c5b62b4e844d194)
