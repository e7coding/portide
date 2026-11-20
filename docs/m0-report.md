# Portide M0 技术验证报告

> 环境：桌面 = Linux x86_64 / Go 1.26；iPhone = （型号 / iOS 版本）；Mac = （型号 / Xcode 版本）

| 编号 | 验证项 | 通过标准 | 结果数据 | 结论 |
|---|---|---|---|---|
| S1 | QUIC 双向 TLS + 公钥白名单 | 成员互通、非成员握手失败、iPhone↔桌面回显 32 MiB | | |
| S2 | modernc SQLite on iOS | 真机 journal=wal、行数累加 | | |
| S3 | Go mDNS ↔ iOS Bonjour | 双向可发现 | | |
| S4 | CPace 配对码 | 同码同密钥、错码可识别；库维护风险评估 | | |
| S5 | BLAKE3 吞吐 | iPhone ≥ 200 MiB/s | | |
| S6 | Share Extension 内嵌 Go 核心 | 扩展内回显 8 MiB 成功，未被系统终止 | | |

## 详细记录

### S1 QUIC 双向 TLS（桌面部分）

- go test ./quicpin -v 输出：
  - === RUN   TestMembersEcho64MiB
    - --- PASS: TestMembersEcho64MiB (0.60s)
    - echo 64 MiB in 401ms (159.8 MiB/s each way)
  - === RUN   TestClientRejectsNonMemberServer
    - --- PASS: TestClientRejectsNonMemberServer (0.01s)
    - rejected as expected: CRYPTO_ERROR 0x12a (local): quicpin: peer is not a member
  - === RUN   TestServerRejectsNonMemberClient
    - --- PASS: TestServerRejectsNonMemberClient (0.00s)
    - rejected as expected: CRYPTO_ERROR 0x12a (remote): tls: bad certificate
- iOS 交叉编译：IOS_BUILD_OK

### S2 SQLite（桌面部分）

- go test ./sqliteprobe -v 输出：
  - === RUN   TestProbePersistsWithWAL
    - sqlite=3.53.4 journal=wal rows=1000 insert1000=9.288ms
    - --- PASS: TestProbePersistsWithWAL (0.08s)
- iOS 交叉编译：IOS_BUILD_OK

### S3 mDNS（桌面部分）

- go test ./mdnsprobe -v 输出（PORTIDE_MDNS_TEST=1）：
  - === RUN   TestAnnounceBrowseSameHost
    - discovered 7687785b0a138ef2c61af76c at [192.168.1.50 fe80::a72e:3ba5:6555:546d]:7443 on enp4s0f2np2
    - --- PASS: TestAnnounceBrowseSameHost (0.21s)
  - === RUN   TestTokenShape
    - --- PASS: TestTokenShape (0.00s)
  - === RUN   TestTokenChangesWithWindowKeyAndDevice
    - --- PASS: TestTokenChangesWithWindowKeyAndDevice (0.00s)
  - === RUN   TestMatchToleratesOneWindowSkew
    - --- PASS: TestMatchToleratesOneWindowSkew (0.00s)
  - === RUN   TestMatchRejectsOutsider
    - --- PASS: TestMatchRejectsOutsider (0.00s)
  - ok portide/spikes/mdnsprobe 0.214s
- iOS 交叉编译：IOS_BUILD_OK
- avahi-browse 验证：本机（桌面审查环境）未安装 avahi-utils，`avahi-browse` 命令不存在，无法用它交叉验证 `mdns-probe -mode announce` 发布的记录；`mdns-probe -mode announce` 本身运行正常，打印的实例名为匿名令牌（如 `6754fdf057b24dea75256fc9._portide._udp`），未见真实主机名。iPhone 端 NWListener 是否在浏览结果中带设备名仍需 Task 8 用真机 avahi-browse 或 Bonjour Browser 实测。

### S4 CPace

- 测试输出：
  - === RUN   TestSameCodeSameKeyAndTagsVerify
    - --- PASS: TestSameCodeSameKeyAndTagsVerify (0.00s)
  - === RUN   TestWrongCodeDetectedByConfirmTag
    - --- PASS: TestWrongCodeDetectedByConfirmTag (0.00s)
  - === RUN   TestSelfTest
    - cpace ok in 452µs (msgA=48B msgB=32B)
    - --- PASS: TestSelfTest (0.00s)
  - ok portide/spikes/pake 0.008s
- 库状态：filippo.io/cpace 只有 2021-01-01 的伪版本，基于 draft-haase-cpace-01，此后未更新；当前 CFRG 草案已有较大演进（例如消息格式、transcript 构造）。
- 初步结论：机制可行（同码同密钥、错码通过确认标签识别）。正式实现时倾向于按最新 CFRG 草案基于 `github.com/gtank/ristretto255` 自己实现，并用草案附带的测试向量校验；最终决定在 Task 9 中做出。

### S5 BLAKE3（桌面部分）

- 测试输出：
  - === RUN   TestBlake3EmptyVector
    - --- PASS: TestBlake3EmptyVector (0.00s)
  - === RUN   TestThroughputReport
    - blake3 256 MiB in 59ms (4314 MiB/s)
    - --- PASS: TestThroughputReport (0.06s)
  - ok portide/spikes/hashprobe 0.072s
- 注意：amd64 上有汇编加速；arm64（iPhone）上是否有加速，以 Task 8 的真机数据为准。

## 已知注意事项（总审查）

- mDNS 主机名：dnssd 默认用真实主机名填 SRV/A 记录，已改为使用令牌作为主机名；iPhone 端 NWListener 是否在记录中带设备名需在 Task 8 用 avahi-browse 实测。
- 会话恢复：crypto/tls 恢复会话时不调用 VerifyPeerCertificate；spike 已在服务端禁用会话票据；M1 正式实现必须用 VerifyConnection 或禁用票据，且不能为了 0-RTT 引入会话缓存而绕过成员校验。
- 确认标签方向：S4 中双方都用 "initiator" 计算标签，M1 必须按方向区分（initiator/responder），防止反射攻击。
- S5 吞吐是复用同一个 1 MiB 热缓存的纯计算速度，代表上限。

## 对设计方案的修订

（Task 9 填写）
