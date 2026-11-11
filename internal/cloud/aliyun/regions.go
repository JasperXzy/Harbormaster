package aliyun

// Initial catalog from successful DescribeRegions reads on 2026-11-11. Once an
// account has synced, GET /regions prefers its saved catalog. Availability is
// still checked by each product's API; no GET request calls the cloud.
func InitialRegions() []map[string]any {
	ids := []string{"cn-qingdao", "cn-beijing", "cn-zhangjiakou", "cn-huhehaote", "cn-wulanchabu", "cn-hangzhou", "cn-shanghai", "cn-nanjing", "cn-shenzhen", "cn-heyuan", "cn-guangzhou", "cn-fuzhou", "cn-wuhan-lr", "cn-zhongwei", "cn-chengdu", "cn-hongkong", "ap-northeast-1", "ap-northeast-2", "ap-southeast-1", "ap-southeast-8", "ap-southeast-3", "ap-southeast-6", "ap-southeast-5", "ap-southeast-7", "us-east-1", "us-west-1", "na-south-1", "sa-east-1", "eu-west-1", "eu-west-2", "eu-west-3", "me-east-1", "eu-central-1"}
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		out = append(out, map[string]any{"id": id, "mainland": mainland(id)})
	}
	return out
}
