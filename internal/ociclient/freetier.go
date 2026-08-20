package ociclient

// 永久免费(Always Free)额度。
//
// 这是全项目唯一的定义处。前端另有一份 web/src/lib/freetier.ts，改这里时
// 必须同步改那边——两份数字对不上会让界面和校验各说各话。
//
// **Oracle 会不打招呼地改这些数字。** 2026-06-15 起 Ampere A1 从
// 4 OCPU / 24 GB 砍到 2 OCPU / 12 GB，没有公告、没有博客，只给用户发了
// 邮件，并从 2026-08-18 起终止超出新限额的永久免费实例。所以：
//
//   - 这些常量只用于「预设」与「提示文案」，绝不用于校验。
//     真实上限一律以 limits 接口返回的值为准——那是账号自己的数字，
//     不会因为本表滞后而出错。
//   - 每个常量都标注核对日期。看到日期久远就该怀疑它。
//
// 核对日期：2026-08-18
// 来源：https://docs.oracle.com/en-us/iaas/Content/FreeTier/freetier_topic-Always_Free_Resources.htm
const (
	// AlwaysFreeARMOcpus 是永久免费的 Ampere A1 OCPU 总数。
	AlwaysFreeARMOcpus = 2
	// AlwaysFreeARMMemoryGB 是永久免费的 Ampere A1 内存总量。
	AlwaysFreeARMMemoryGB = 12

	// LegacyFreeARMOcpus 是 2026-06-15 之前的永久免费 ARM 上限。
	//
	// 保留它不是为了怀旧：据用户报告，升级为 PAY_AS_YOU_GO 的账号仍按这个
	// 额度免费使用 ARM。这个说法未见 Oracle 正式确认，因此只用于给升级号
	// 提供一档预设，不作为任何判断依据。
	LegacyFreeARMOcpus    = 4
	LegacyFreeARMMemoryGB = 24

	// AlwaysFreeMicroInstances 是永久免费的 E2.1.Micro 台数。
	AlwaysFreeMicroInstances = 2
	// AlwaysFreeBlockGB 是永久免费的块存储总量。
	AlwaysFreeBlockGB = 200
)
