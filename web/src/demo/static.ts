/**
 * 后端写死的静态清单（渠道类型、可订阅事件、规则模板、创建预设、守候限制、默认设置）。
 *
 * 由本地后端的真实响应生成，不是手抄的。后端改了这些清单时，重新生成一遍：
 * 启动后端、登录，把对应接口的响应粘进来。
 */
/* eslint-disable */
import type * as D from '@/api/types'
import { locale } from '@/i18n'

type Static = {
  channelKinds: D.ChannelKindDTO[]
  notifyEvents: D.NotifyEventDTO[]
  ruleTemplates: D.RuleTemplateDTO[]
  launchPresets: D.LaunchPresetDTO[]
  huntLimits: D.HuntLimitsDTO
  probeIntervalSeconds: number
  settings: D.SettingsDTO
}

const ZH = {
  "channelKinds": [
    {
      "kind": "telegram",
      "label": "Telegram",
      "fields": [
        {
          "key": "token",
          "label": "Bot Token",
          "required": true,
          "secret": true,
          "hint": "向 @BotFather 申请"
        },
        {
          "key": "chatId",
          "label": "Chat ID",
          "required": true,
          "secret": false,
          "hint": "向 @userinfobot 发消息可获取"
        }
      ]
    },
    {
      "kind": "wecom",
      "label": "企业微信",
      "fields": [
        {
          "key": "webhook",
          "label": "群机器人 Webhook",
          "required": true,
          "secret": true
        }
      ]
    },
    {
      "kind": "dingtalk",
      "label": "钉钉",
      "fields": [
        {
          "key": "webhook",
          "label": "群机器人 Webhook",
          "required": true,
          "secret": true
        },
        {
          "key": "secret",
          "label": "加签密钥",
          "required": false,
          "secret": true,
          "hint": "启用加签时填写"
        }
      ]
    },
    {
      "kind": "email",
      "label": "邮件",
      "fields": [
        {
          "key": "host",
          "label": "SMTP 服务器",
          "required": true,
          "secret": false
        },
        {
          "key": "port",
          "label": "端口",
          "required": true,
          "secret": false,
          "hint": "587（STARTTLS）或 465（SSL）"
        },
        {
          "key": "security",
          "label": "加密方式",
          "required": false,
          "secret": false,
          "options": [
            {
              "value": "",
              "label": "自动（465 用 SSL，其余尝试 STARTTLS）"
            },
            {
              "value": "ssl",
              "label": "SSL/TLS（隐式加密）"
            },
            {
              "value": "starttls",
              "label": "STARTTLS（必须升级加密）"
            }
          ]
        },
        {
          "key": "username",
          "label": "用户名",
          "required": true,
          "secret": false
        },
        {
          "key": "password",
          "label": "密码",
          "required": true,
          "secret": true
        },
        {
          "key": "from",
          "label": "发件人",
          "required": true,
          "secret": false
        },
        {
          "key": "to",
          "label": "收件人",
          "required": true,
          "secret": false,
          "hint": "多个用逗号分隔"
        }
      ]
    },
    {
      "kind": "webhook",
      "label": "自定义 Webhook",
      "fields": [
        {
          "key": "url",
          "label": "URL",
          "required": true,
          "secret": false
        },
        {
          "key": "method",
          "label": "方法",
          "required": false,
          "secret": false,
          "hint": "留空为 POST"
        }
      ]
    }
  ],
  "notifyEvents": [
    {
      "key": "account.auth",
      "label": "账号认证失败",
      "description": "API 密钥失效或权限不足"
    },
    {
      "key": "instance.created",
      "label": "实例创建完成",
      "description": "新实例已就绪并分配到公网 IP"
    },
    {
      "key": "danger.operation",
      "label": "危险操作已执行",
      "description": "终止实例、删除账号、更换 IP 等操作"
    },
    {
      "key": "hunt.succeeded",
      "label": "守候任务抢到实例",
      "description": "容量守候任务成功创建出实例"
    },
    {
      "key": "hunt.stopped",
      "label": "守候任务停止",
      "description": "任务因配额已满、凭据失效或到期而停止"
    },
    {
      "key": "capacity.changed",
      "label": "容量状态变化",
      "description": "监控的规格从没有容量变成有容量（或反之）"
    }
  ],
  "ruleTemplates": [
    {
      "key": "ssh",
      "label": "SSH",
      "direction": "ingress",
      "protocol": "6",
      "port": 22,
      "description": "远程登录",
      "dangerous": false
    },
    {
      "key": "http",
      "label": "HTTP",
      "direction": "ingress",
      "protocol": "6",
      "port": 80,
      "description": "网站服务",
      "dangerous": false
    },
    {
      "key": "https",
      "label": "HTTPS",
      "direction": "ingress",
      "protocol": "6",
      "port": 443,
      "description": "加密网站服务",
      "dangerous": false
    },
    {
      "key": "icmp",
      "label": "ICMP",
      "direction": "ingress",
      "protocol": "1",
      "port": 0,
      "description": "允许 ping",
      "dangerous": false
    },
    {
      "key": "all",
      "label": "全部放行",
      "direction": "ingress",
      "protocol": "all",
      "port": 0,
      "description": "放行所有入站流量。除非你清楚自己在做什么，否则不要开启。",
      "dangerous": true
    },
    {
      "key": "egress-all",
      "label": "出站全放行",
      "direction": "egress",
      "protocol": "all",
      "port": 0,
      "description": "放行所有出站流量。这是 OCI 的默认配置——没有出站规则时实例无法访问外网。",
      "dangerous": false
    }
  ],
  "launchPresets": [
    {
      "key": "arm-free",
      "label": "免费额度 ARM（满配）",
      "shape": "VM.Standard.A1.Flex",
      "ocpus": 2,
      "memoryInGbs": 12,
      "bootGb": 50,
      "description": "Ampere A1，永久免费额度上限（2026-06-15 起为 2C12G）。容量紧张时较难开出。",
      "freeTier": true
    },
    {
      "key": "arm-half",
      "label": "免费额度 ARM（半配）",
      "shape": "VM.Standard.A1.Flex",
      "ocpus": 1,
      "memoryInGbs": 6,
      "bootGb": 50,
      "description": "占用一半免费额度，可以开两台。",
      "freeTier": true
    },
    {
      "key": "arm-legacy",
      "label": "ARM 4C24G（仅升级号）",
      "shape": "VM.Standard.A1.Flex",
      "ocpus": 4,
      "memoryInGbs": 24,
      "bootGb": 50,
      "description": "2026-06-15 前的免费上限。升级号（PAYG）据报仍可免费使用；永久免费号超出新限额的实例已从 2026-08-18 起被 Oracle 回收。",
      "freeTier": false
    },
    {
      "key": "amd-free",
      "label": "免费额度 AMD",
      "shape": "VM.Standard.E2.1.Micro",
      "ocpus": 1,
      "memoryInGbs": 1,
      "bootGb": 50,
      "description": "配置很低但基本随时能开，适合做跳板机。",
      "freeTier": true
    }
  ],
  "huntLimits": {
    "defaultIntervalSeconds": 60,
    "minIntervalSeconds": 30,
    "warnIntervalSeconds": 60
  },
  "probeIntervalSeconds": 300,
  "settings": {
    "allowTerminate": true,
    "allowBulkActions": true,
    "requireTotpForDanger": false,
    "syncIntervalMinutes": 5,
    "checkIntervalHours": 6,
    "auditRetentionDays": 0,
    "language": "zh-CN"
  }
} as unknown as Static

const EN = {
  "channelKinds": [
    {
      "kind": "telegram",
      "label": "Telegram",
      "fields": [
        {
          "key": "token",
          "label": "Bot Token",
          "required": true,
          "secret": true,
          "hint": "Get one from @BotFather"
        },
        {
          "key": "chatId",
          "label": "Chat ID",
          "required": true,
          "secret": false,
          "hint": "Message @userinfobot to get it"
        }
      ]
    },
    {
      "kind": "wecom",
      "label": "WeCom",
      "fields": [
        {
          "key": "webhook",
          "label": "Group bot webhook",
          "required": true,
          "secret": true
        }
      ]
    },
    {
      "kind": "dingtalk",
      "label": "DingTalk",
      "fields": [
        {
          "key": "webhook",
          "label": "Group bot webhook",
          "required": true,
          "secret": true
        },
        {
          "key": "secret",
          "label": "Signing secret",
          "required": false,
          "secret": true,
          "hint": "Fill in if signing is enabled"
        }
      ]
    },
    {
      "kind": "email",
      "label": "Email",
      "fields": [
        {
          "key": "host",
          "label": "SMTP server",
          "required": true,
          "secret": false
        },
        {
          "key": "port",
          "label": "Port",
          "required": true,
          "secret": false,
          "hint": "587 (STARTTLS) or 465 (SSL)"
        },
        {
          "key": "security",
          "label": "Encryption",
          "required": false,
          "secret": false,
          "options": [
            {
              "value": "",
              "label": "Auto (SSL on 465, otherwise try STARTTLS)"
            },
            {
              "value": "ssl",
              "label": "SSL/TLS (implicit)"
            },
            {
              "value": "starttls",
              "label": "STARTTLS (upgrade required)"
            }
          ]
        },
        {
          "key": "username",
          "label": "Username",
          "required": true,
          "secret": false
        },
        {
          "key": "password",
          "label": "Password",
          "required": true,
          "secret": true
        },
        {
          "key": "from",
          "label": "From",
          "required": true,
          "secret": false
        },
        {
          "key": "to",
          "label": "To",
          "required": true,
          "secret": false,
          "hint": "Separate multiple addresses with commas"
        }
      ]
    },
    {
      "kind": "webhook",
      "label": "Custom webhook",
      "fields": [
        {
          "key": "url",
          "label": "URL",
          "required": true,
          "secret": false
        },
        {
          "key": "method",
          "label": "Method",
          "required": false,
          "secret": false,
          "hint": "Defaults to POST"
        }
      ]
    }
  ],
  "notifyEvents": [
    {
      "key": "account.auth",
      "label": "Account authentication failed",
      "description": "API key invalid or insufficient permissions"
    },
    {
      "key": "instance.created",
      "label": "Instance created",
      "description": "A new instance is ready with a public IP"
    },
    {
      "key": "danger.operation",
      "label": "Dangerous action performed",
      "description": "Terminating instances, deleting accounts, changing IPs and similar actions"
    },
    {
      "key": "hunt.succeeded",
      "label": "Capacity hunt got an instance",
      "description": "A capacity hunt task successfully created an instance"
    },
    {
      "key": "hunt.stopped",
      "label": "Capacity hunt stopped",
      "description": "The task stopped because quota ran out, credentials failed or it expired"
    },
    {
      "key": "capacity.changed",
      "label": "Capacity changed",
      "description": "A watched shape went from no capacity to available (or back)"
    }
  ],
  "ruleTemplates": [
    {
      "key": "ssh",
      "label": "SSH",
      "direction": "ingress",
      "protocol": "6",
      "port": 22,
      "description": "Remote login",
      "dangerous": false
    },
    {
      "key": "http",
      "label": "HTTP",
      "direction": "ingress",
      "protocol": "6",
      "port": 80,
      "description": "Web server",
      "dangerous": false
    },
    {
      "key": "https",
      "label": "HTTPS",
      "direction": "ingress",
      "protocol": "6",
      "port": 443,
      "description": "Secure web server",
      "dangerous": false
    },
    {
      "key": "icmp",
      "label": "ICMP",
      "direction": "ingress",
      "protocol": "1",
      "port": 0,
      "description": "Allow ping",
      "dangerous": false
    },
    {
      "key": "all",
      "label": "Allow all",
      "direction": "ingress",
      "protocol": "all",
      "port": 0,
      "description": "Allows all inbound traffic. Don't enable this unless you know what you're doing.",
      "dangerous": true
    },
    {
      "key": "egress-all",
      "label": "Allow all egress",
      "direction": "egress",
      "protocol": "all",
      "port": 0,
      "description": "Allows all outbound traffic. This is OCI's default — without an egress rule instances can't reach the internet.",
      "dangerous": false
    }
  ],
  "launchPresets": [
    {
      "key": "arm-free",
      "label": "Always Free ARM (full)",
      "shape": "VM.Standard.A1.Flex",
      "ocpus": 2,
      "memoryInGbs": 12,
      "bootGb": 50,
      "description": "Ampere A1 at the Always Free limit (2C12G since 2026-06-15). Hard to get when capacity is tight.",
      "freeTier": true
    },
    {
      "key": "arm-half",
      "label": "Always Free ARM (half)",
      "shape": "VM.Standard.A1.Flex",
      "ocpus": 1,
      "memoryInGbs": 6,
      "bootGb": 50,
      "description": "Uses half the free allowance, so you can run two.",
      "freeTier": true
    },
    {
      "key": "arm-legacy",
      "label": "ARM 4C24G (paid accounts only)",
      "shape": "VM.Standard.A1.Flex",
      "ocpus": 4,
      "memoryInGbs": 24,
      "bootGb": 50,
      "description": "The free limit before 2026-06-15. Reportedly still free on paid (PAYG) accounts; on Always Free accounts, instances over the new limit have been reclaimed by Oracle since 2026-08-18.",
      "freeTier": false
    },
    {
      "key": "amd-free",
      "label": "Always Free AMD",
      "shape": "VM.Standard.E2.1.Micro",
      "ocpus": 1,
      "memoryInGbs": 1,
      "bootGb": 50,
      "description": "Very small but almost always available — good as a jump host.",
      "freeTier": true
    }
  ],
  "huntLimits": {
    "defaultIntervalSeconds": 60,
    "minIntervalSeconds": 30,
    "warnIntervalSeconds": 60
  },
  "probeIntervalSeconds": 300,
  "settings": {
    "allowTerminate": true,
    "allowBulkActions": true,
    "requireTotpForDanger": false,
    "syncIntervalMinutes": 5,
    "checkIntervalHours": 6,
    "auditRetentionDays": 0,
    "language": "zh-CN"
  }
} as unknown as Static

/** 按当前界面语言取静态清单（后端也按 Accept-Language 返回对应语言）。 */
export const staticData = (): Static => (locale.value === 'en' ? EN : ZH)
