// 本文件不再声明任何符号，只记录「杂项端点」的拆分去向。
//
// 早期所有非推理端点都堆在同一个 extras.go（845 行）里，
// 现按职责拆成多个文件，便于定位与评审：
//
//	handlers_skills.go         技能清单、能力自发现（/v1/skills、/v1/capabilities）
//	handlers_conversations.go  会话列表与单会话操作（/v1/conversations*）
//	handlers_credits.go        积分、流水、签到、任务奖励（/v1/credits*、/v1/checkin、/v1/tasks*）
//	autorewards.go             后台自动签到 / 自动领取调度（StartAutoRewards）
//	upstream.go                选号、超时、日志、错误响应的统一入口（runUpstream）
//	collector.go               一次对话的文本与结构化产物汇总（responseCollector）
//	music.go                   音频与歌词端点（/v1/music/*）
//
// 保留本文件是为了让「extras.go 去哪了」这个问题有一个可检索的答案；
// 新增端点请直接放进对应职责的文件，不要再回到这里。
package server
