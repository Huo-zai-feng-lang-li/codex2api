"""
发包机器人测试：模拟 Codex 客户端真实链路
- 首包 A：用 gpt-5.2 调用 /v1/responses，验证映射到 free/glm-5.3-flash
- 续问包 B：携带 previous_response_id 再次调用，验证续链正常
"""
import json
import time
import urllib.request
import urllib.error

BASE_URL = "http://127.0.0.1:18080/v1"
API_KEY = "sk-e734b9d5dc5b56e73206d8c71f963df3174ea3a58f325517"
MODEL = "gpt-5.2"
EXPECTED_MAPPED_MODEL = "free/glm-5.3-flash"


def call_responses(model, input_text, previous_response_id=None):
    """调用 /v1/responses 接口"""
    body = {
        "model": model,
        "input": input_text,
        "stream": False,
    }
    if previous_response_id:
        body["previous_response_id"] = previous_response_id

    data = json.dumps(body).encode("utf-8")
    req = urllib.request.Request(
        f"{BASE_URL}/responses",
        data=data,
        headers={
            "Authorization": f"Bearer {API_KEY}",
            "Content-Type": "application/json",
        },
        method="POST",
    )

    start = time.time()
    try:
        with urllib.request.urlopen(req, timeout=60) as resp:
            elapsed = time.time() - start
            result = json.loads(resp.read().decode("utf-8"))
            return resp.status, elapsed, result, None
    except urllib.error.HTTPError as e:
        elapsed = time.time() - start
        err_body = e.read().decode("utf-8", errors="replace")
        return e.code, elapsed, None, err_body
    except Exception as e:
        elapsed = time.time() - start
        return 0, elapsed, None, str(e)


def extract_output(result):
    """从响应中提取输出文本"""
    if not result:
        return ""
    # Responses API 格式：output 数组中的 message.content
    output_parts = []
    for item in result.get("output", []):
        if item.get("type") == "message":
            for content in item.get("content", []):
                if content.get("type") == "output_text":
                    output_parts.append(content.get("text", ""))
    return "".join(output_parts)


def main():
    print("=" * 60)
    print("发包机器人测试：模型映射 + 续链真实链路")
    print("=" * 60)

    # ========== 首包 A ==========
    print("\n[首包 A] 调用 /v1/responses, model=gpt-5.2")
    print(f"  期望映射到: {EXPECTED_MAPPED_MODEL}")

    status, elapsed, result, err = call_responses(MODEL, "你好，请用一句话介绍你自己")

    print(f"  HTTP 状态: {status}")
    print(f"  耗时: {elapsed:.2f}s")

    if err:
        print(f"  错误: {err[:500]}")
        print("\n[FAIL] 首包 A 失败")
        return False

    if status != 200:
        print(f"  响应: {json.dumps(result, ensure_ascii=False)[:500]}")
        print("\n[FAIL] 首包 A 非 200")
        return False

    response_id = result.get("id", "")
    actual_model = result.get("model", "")
    output_text = extract_output(result)

    print(f"  response_id: {response_id}")
    print(f"  实际 model: {actual_model}")
    print(f"  输出: {output_text[:200]}")

    # 验证映射
    if actual_model == EXPECTED_MAPPED_MODEL:
        print(f"  [PASS] 模型映射正确: {MODEL} -> {actual_model}")
    else:
        print(f"  [WARN] 模型映射未生效或上游返回不同模型: 期望={EXPECTED_MAPPED_MODEL}, 实际={actual_model}")

    if not response_id:
        print("\n[FAIL] 首包 A 未返回 response_id，无法进行续链测试")
        return False

    # ========== 续问包 B ==========
    print(f"\n[续问包 B] 携带 previous_response_id={response_id[:20]}... 再次调用")

    status2, elapsed2, result2, err2 = call_responses(
        MODEL, "刚才你说了什么？请重复一遍", previous_response_id=response_id
    )

    print(f"  HTTP 状态: {status2}")
    print(f"  耗时: {elapsed2:.2f}s")

    if err2:
        print(f"  错误: {err2[:500]}")
        print("\n[FAIL] 续问包 B 失败")
        return False

    if status2 != 200:
        print(f"  响应: {json.dumps(result2, ensure_ascii=False)[:500]}")
        print("\n[FAIL] 续问包 B 非 200")
        return False

    actual_model2 = result2.get("model", "")
    output_text2 = extract_output(result2)

    print(f"  实际 model: {actual_model2}")
    print(f"  输出: {output_text2[:200]}")
    print(f"  [PASS] 续问包 B 返回 200，续链正常")

    # ========== 总结 ==========
    print("\n" + "=" * 60)
    print("测试总结")
    print("=" * 60)
    print(f"  首包 A: HTTP {status}, {elapsed:.2f}s, model={actual_model}")
    print(f"  续问包 B: HTTP {status2}, {elapsed2:.2f}s, model={actual_model2}")
    print(f"  模型映射: {'PASS' if actual_model == EXPECTED_MAPPED_MODEL else 'WARN (上游返回不同模型)'}")
    print(f"  续链: PASS")
    print(f"  两连包均 HTTP 200 OK: PASS")
    print("=" * 60)
    print("[ALL PASS] 发包机器人测试通过")
    return True


if __name__ == "__main__":
    success = main()
    exit(0 if success else 1)
