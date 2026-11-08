import type { ProviderSummary } from "@hermesh/her-protocol";

// Preview fixtures for the built-in subscription products.
export const demoTokenPlans: ProviderSummary[] = [
  {
    "id": "bailian-token-plan",
    "name": "Bailian Token Plan",
    "auth": "api_key",
    "credential": "missing",
    "key_env": [
      "BAILIAN_TOKEN_PLAN_API_KEY"
    ],
    "plan": {
      "key_url": "https://bailian.console.aliyun.com/cn-beijing/subscription/overview",
      "base_url": "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1",
      "api": "openai-chat",
      "dynamic_models": true
    }
  },
  {
    "id": "tencent-token-plan",
    "name": "Tencent Cloud Token Plan",
    "auth": "api_key",
    "credential": "missing",
    "key_env": [
      "TENCENT_TOKEN_PLAN_API_KEY"
    ],
    "plan": {
      "key_url": "https://console.cloud.tencent.com/tokenhub",
      "base_url": "https://tokenhub.tencentmaas.com/plan/v3",
      "api": "openai-chat",
      "dynamic_models": false
    }
  },
  {
    "id": "volcengine-agent-plan",
    "name": "Volcengine Ark Agent Plan",
    "auth": "api_key",
    "credential": "missing",
    "key_env": [
      "VOLCENGINE_AGENT_PLAN_API_KEY"
    ],
    "plan": {
      "key_url": "https://ark.volcengine.com/region:cn-beijing/openManagement",
      "base_url": "https://ark.cn-beijing.volces.com/api/plan/v3",
      "api": "openai-chat",
      "dynamic_models": false
    }
  },
  {
    "id": "volcengine-coding-plan",
    "name": "Volcengine Ark Coding Plan",
    "auth": "api_key",
    "credential": "missing",
    "key_env": [
      "VOLCENGINE_CODING_PLAN_API_KEY"
    ],
    "plan": {
      "key_url": "https://ark.volcengine.com/region:cn-beijing/openManagement",
      "base_url": "https://ark.cn-beijing.volces.com/api/coding/v3",
      "api": "openai-chat",
      "dynamic_models": false
    }
  },
  {
    "id": "zai-coding-plan",
    "name": "Z.ai GLM Coding Plan",
    "auth": "api_key",
    "credential": "missing",
    "key_env": [
      "ZAI_CODING_PLAN_API_KEY"
    ],
    "plan": {
      "key_url": "https://z.ai/manage-apikey/apikey-list",
      "base_url": "https://api.z.ai/api/coding/paas/v4",
      "api": "openai-chat",
      "dynamic_models": false
    }
  },
  {
    "id": "zhipu-coding-plan",
    "name": "Zhipu GLM Coding Plan",
    "auth": "api_key",
    "credential": "missing",
    "key_env": [
      "ZHIPU_CODING_PLAN_API_KEY"
    ],
    "plan": {
      "key_url": "https://www.bigmodel.cn/glm-coding",
      "base_url": "https://open.bigmodel.cn/api/coding/paas/v4",
      "api": "openai-chat",
      "dynamic_models": false
    }
  }
];
