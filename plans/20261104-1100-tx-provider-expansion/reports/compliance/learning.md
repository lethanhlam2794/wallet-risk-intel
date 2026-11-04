# Codex Learning Pattern Ledger v9 — Sổ theo dõi Pattern học tập

> **Mục đích**: Lưu các failure mechanism và quy tắc phòng ngừa đã có bằng
> chứng. Source code và tests vẫn là ground truth; retrieval chỉ đưa ra ứng
> viên, không tự quyết định deduplication hoặc promotion.
>
> **Canonical owner**: file này cùng `learning-metadata.json`. TencentDB skill
> chỉ nhận các pattern `Status: active` và `Review: approved` thông qua `--apply`.
>
> **Layout (2026-09-29)**: File gồm 2 tầng — **Nguyên lý tổng quát `[G-XXX]`**
> (6 invariants, ghi kỹ, là nội dung chính reviewer đọc) và **Patterns `[P-XXX]`**
> (stub gọn: lesson + prevention 1 dòng; evidence đầy đủ nằm trong
> `learning-metadata.json` occurrences, bản text gốc trước tinh gọn nằm trong
> `learning-patterns-archive-20260929.md`). Machine metadata (`Type`, `Root cause`,
> `Scope`, `Failure mechanism`, `Review`, `Reviewed by`, `Review hash`,
> `Verified at`, `Occurrences`, freshness) nằm trong `learning-metadata.json`,
> keyed theo pattern ID. CLI merge hai nguồn theo ID; metadata JSON là
> machine-of-record.
>
> **Ai được ghi**: Implementer tạo `draft/pending` sau khi kết quả đã được kiểm
> chứng. Chỉ người dùng, asset owner hoặc reviewer được đổi thành
> `active/approved`. Không ghi secrets, phỏng đoán hoặc raw third-party response.

---

## Quy trình v9 bắt buộc

### Phạm vi ràng buộc và giới hạn kiểm chứng

Entry chỉ ràng buộc với tư cách ledger policy khi `active/approved`, review
fingerprint hợp lệ và scope đã được đối chiếu với source/test của task.
Draft/pending và các G đang chờ cross-review là ứng viên evidence, không tự
trở thành policy toàn cục. Các yêu cầu an toàn độc lập của user/project/global
vẫn ràng buộc, kể cả khi pattern được tham chiếu còn draft (ví dụ cô lập DB test).
Không tự approve candidate; retrieval score không chứng minh applicability.

CLI hiện parse/enforce các P-pattern; G-principles chưa có machine-enforcement
hay publication contract. Không gọi G là đã được audit/approve bởi một kết quả
CLI chỉ kiểm tra P. Giữ G ở trạng thái chờ review, không tự promote.

### 1. Tạo candidate sau kiểm chứng

Chỉ tạo candidate khi đã có source/test/runner evidence. Candidate mới ghi
human fields vào `learning.md` và machine fields vào `learning-metadata.json`
(missing/duplicate/unknown ID là lỗi audit fail-closed). Bắt buộc:

- `Type`: `incident`, `near-miss`, `preventive-rule` hoặc `decision`.
- `Status: draft` và `Review: pending`.
- Một `Root cause` kebab-case duy nhất.
- Một `Failure mechanism` duy nhất.
- Ít nhất một occurrence gồm ngày, project, evidence và verification.
- `Verified at` là ngày evidence gần nhất được kiểm chứng.
- Bài học mô tả invariant; phòng ngừa mô tả hành động kiểm tra được.

Prompt hoặc thiết kế chưa chạy thực tế chỉ được phân loại `preventive-rule`;
không được ghi như incident.

### 2. One-root-cause gate

Trước khi ghi, bắt buộc hỏi:

> Nếu tách hai phần của bài học ra, chúng có thể xảy ra độc lập không?

Nếu có, phải tách thành hai pattern. Một pattern có thể có nhiều biểu hiện và
nhiều biện pháp phòng ngừa, nhưng chỉ có một failure mechanism/root-cause class.

### 3. Retrieval chỉ tìm ứng viên

Chạy:

```bash
~/.local/bin/learning-ledger-sync.sh --check '<mô tả root cause>'
```

Diễn giải kết quả:

- `EXACT_CLASS`: class xuất hiện trực tiếp trong query; vẫn phải so evidence.
- `CANDIDATE`: ứng viên lexical đủ confidence; reviewer quyết định.
- `AMBIGUOUS`: nhiều ứng viên gần nhau; giữ draft cho đến khi phân định được.
- `NO_CONFIDENT_MATCH`: giữ draft; không tự kết luận đây là class mới.

Chỉ thêm occurrence khi failure mechanism và invariant bị vi phạm thực sự
giống nhau. Cùng triệu chứng nhưng khác mechanism phải là pattern khác.

### 4. Occurrence là nguồn của counter

Không sửa `Số lần`, `Lần đầu`, `Lần cuối` bằng tay. CLI tự suy ra từ danh sách
`Occurrences`. Mỗi incident/project thực tế là một occurrence có evidence riêng.

### 5. Review và đồng bộ

1. Reviewer xác minh evidence, root cause, scope, dedup và one-root-cause gate.
2. Reviewer/người dùng khác với tác giả candidate ghi `Reviewed by`, chạy
   `--review-hash P-NNN` và ghi `Review hash`, rồi đổi `Review: approved` và
   `Status: active`. Agent không được tự approve candidate do chính nó tạo.
3. Chạy `--audit`; lỗi local phải được sửa trước khi đồng bộ.
4. Chạy `--apply`. Script chỉ sync các entry `active/approved`, dùng optimistic
   version và xác minh lại content hash.
5. Draft/pending không xuất hiện trong `--summary`, `--read`, `--check` hoặc
   TencentDB team skill.

Khi thêm occurrence hoặc sửa evidence/bài học/phòng ngừa của một pattern đang
active, phải đưa pattern về `draft/pending`, cập nhật `Verified at` sau review,
rồi xin approval/hash mới trước `--apply`; hash cũ sẽ mismatch và approval cũ
không bao phủ nội dung mới.

`Review hash` bind approval với title, type, root cause, scope, failure
mechanism, stable pattern ID, verification date, occurrences, lesson và prevention. Hash bảo vệ
freshness/integrity, nhưng danh tính trong `Reviewed by` vẫn là trust boundary
thủ công: repository/host phải giới hạn quyền sửa canonical file.

Lifecycle: `draft` → `verified` (evidence đã kiểm tra, chưa được publish) →
`active`; sau đó có thể chuyển `deprecated`, `superseded`, `invalid` hoặc
`archived`. Chỉ `active/approved` được retrieval và publication sử dụng.

V9 cố ý chỉ nhận biến môi trường namespaced `MEMORY_LEDGER_*` (hoặc
`MEMORY_CORE_GATEWAY_API_KEY`/file env đã duyệt); các tên generic cũ như
`API_KEY` và `GATEWAY_URL` bị bỏ để tránh gửi nhầm credential sang endpoint
khác. Gateway URL dùng `MEMORY_LEDGER_GATEWAY_URL`, fallback
`MEMORY_CORE_GATEWAY_URL`; API key dùng `MEMORY_LEDGER_API_KEY`, fallback
`MEMORY_CORE_GATEWAY_API_KEY` hoặc env file đã duyệt.

### 6. Promotion

`Số lần >= 5` hoặc nhóm từ ba pattern liên quan chỉ là tín hiệu review, không
tự động promotion. Chỉ tạo/cập nhật `[G-XXX]` khi root cause ổn định, biện pháp
phòng ngừa đã được kiểm chứng và reviewer xác nhận phạm vi đủ tổng quát.

---

## Nguyên lý tổng quát

> **Cập nhật kiến trúc (2026-09-29)**: Mở rộng từ 3 lên **6 Nguyên lý Bất biến
> Cốt lõi**, bao phủ toàn bộ 51 patterns ([P-001] -> [P-051]). Pattern stubs ở
> cuối file chỉ giữ lesson/prevention 1 dòng; bài học chi tiết đã được distill
> vào từng G. 6 ranh giới: **Runtime Truth**, **Structural Invariants**,
> **Zero-Trust Boundaries**, **Durable State-Machine Safety**,
> **Async Concurrency Ownership**, **Test Surface Truth**.

### [G-001] Runtime Truth & Verification Boundary
- **Type**: principle
- **Status**: promotion-pending
- **Review**: needs-cross-review
- **Root cause**: speculative-deduction-and-unverified-runtime-truth
- **Scope**: runtime-contracts/testing/gateways/third-party-reality/spec-vs-artifact
- **Failure mechanism class**: Quyết định hoặc kết luận được rút ra từ giả định lý thuyết — suy đoán code xuôi dòng, tin spec/fixture thay artifact thật, tin DB preference thay runtime state, tin snapshot cũ sau khi ground truth đã đổi. Khi chạy thật: precondition đầu phễu không qua, third-party trả state khác giả định, thiết bị thật khác spec, mock che giấu contract.
- **Covered patterns**: P-002, P-005, P-007, P-008, P-020, P-021, P-022, P-034, P-037, P-038, P-039, P-040, P-041, P-042, P-046
- **Verified at**: 2026-09-29
- **Bài học tổng quát**: **Runtime Truth > mọi giả định, spec, fixture hay snapshot.** Không kết luận side-effect từ tên hàm/code xuôi dòng nếu chưa chứng minh preconditions đầu luồng. Mọi contract runtime (broker response, ORM projection, bridge payload, thiết bị ngoại vi, thẻ/secure element) phải có ít nhất một kiểm chứng trên instance/artifact thật.
- **Quy tắc bất biến**:
  1. *Cấm suy đoán xuôi dòng*: Trước khi khẳng định "record được tạo / event được bắn / lệnh được đặt", phải trace từ entrypoint và chứng minh mọi precondition (auth, credential validity, lock, balance, entity tồn tại). Thực thể ảo (mock/CRM/virtual credentials) không thể xác thực với third-party thật — quá trình sẽ chết ở gateway, code phía sau không bao giờ chạy. [P-021]
  2. *External state verify bằng response thật*: Dữ liệu broker chỉ được dùng sau khi response xác nhận đồng thời `ok=true`, giá trị hữu hạn và mode/scope đúng yêu cầu; DB preference không phải bằng chứng cho runtime state; không nuốt lỗi chuyển trạng thái. [P-022]
  3. *Wire/protocol format verify trên đối tác thật*: Giữ canonical format ở storage/validation, convert sang wire format đúng một chỗ trước khi gửi; echo-verification phải so toàn bộ payload sau khi parse về canonical — không check phần tử đầu hay `parsed[0]`. [P-046]
  4. *Spec-mandated ≠ hiện diện thật*: File/trường/algorithm mà spec gọi là mandatory vẫn có thể vắng trên thiết bị thật. Fail-closed chỉ đúng khi không còn nguồn chứng minh nào khác — ưu tiên fallback có bằng chứng mật mã (verified artifact) thay vì cứ thiếu là fail. [P-041]
  5. *Visible ID ≠ credential seed*: Định danh in trên vật mang (printed ID, serial) không đồng nhất với seed bên trong chip/secure element. Normalization đặt ở adapter/app layer theo card-family; không nới frozen wire contract của SDK. [P-037]
  6. *Allowlist "closed by design" cần validate trên artifact thật*: Fixture chuẩn spec không bao phủ lựa chọn của từng issuer/quốc gia. Giữ đường chẩn đoán in ra OID/tham số thực nhận trước khi reject; khi mở rộng table giữ nguyên ràng buộc phụ (reject SHA-1/MD5, digest-match). [P-038]
  7. *Parser khoan dung có chọn lọc*: Với dữ liệu thẻ/secure element thật, chấp nhận encode quirks phổ biến (application-tag wrapper, SET/SEQUENCE hoán đổi) nhưng vẫn typed-reject phần còn lại; reject reason đủ chi tiết để phân biệt format-failure vs crypto-failure trong 1 lần đọc log. [P-039]
  8. *Platform provider name bị shadow*: Không tin name lookup trên platform đã strip/shadow provider (Android `"BC"`); truyền provider instance trực tiếp. Bug loại này chỉ lộ trên device/instrumentation test — JVM test xanh không chứng minh gì. [P-040]
  9. *Serialization boundary đổi kiểu ngầm*: Bridge/boundary coerce mọi số thành kiểu thật của transport (RN ReadableMap → Double); coerce trước `when`/cast; test mapper feed đúng kiểu runtime thật. [P-042]
  10. *Ground truth thay đổi theo thời gian*: Contract claim quyết định architecture phải re-verify ngay trước adjudication/finalize — parallel session có thể mutate giữa lúc đọc và lúc viết plan. Khi 2 snapshot mâu thuẫn: `git status`/`git log`/checksum quyết định bản mới hơn, rồi re-read — không chọn tin bên nào. [P-034]
  11. *Doubles/runner phải phản ánh contract thật*: Typed mock theo base-class signature (`Pick`/`satisfies`); raw projection khai báo alias tường minh và đọc đúng key; migration file + đăng ký runner là MỘT thay đổi không tách; mock độc lập không chứng minh được type contract (WRONGTYPE chỉ nổ trên instance thật). [P-002, P-005, P-007, P-008]
  12. *Test environment cũng là runtime truth*: Integration test trên DB chia sẻ làm hỏng dữ liệu thật — môi trường test phải cô lập như invariant độc lập (chi tiết tại G-006). [P-020]
- **Gợi ý học thêm**:
  1. Martin Fowler — Mocks Aren't Stubs & Database Integration Testing Patterns
  2. Fallacies of Distributed Computing & Anti-Corruption Layers
  3. Precondition Verification & Control Flow Integrity
  4. ICAO 9303 real-world eMRTD quirks (JMRTD leniency model)

### [G-002] Structural & Cross-Layer Data Invariants
- **Type**: principle
- **Status**: promotion-pending
- **Review**: needs-cross-review
- **Root cause**: cross-layer-data-invariant-misalignment
- **Scope**: persistence/schema-evolution/identifiers/canonical-forms/units/concurrency-primitives
- **Failure mechanism class**: Một invariant về dữ liệu/shared-state (canonical form, alias, kiểu tham số, đơn vị tiền tệ, audit columns, discriminator, lock token, schema version, constraint semantics) bị khai báo/thực thi lệch pha giữa các tầng (DDL ↔ ORM ↔ application ↔ cache ↔ deploy runner ↔ API contract). Hệ quả: runtime crash (22P02, WRONGTYPE, column missing), mất audit, sai tiền, race/duplicate.
- **Covered patterns**: P-003, P-005, P-006, P-007, P-008, P-010, P-011, P-013, P-015, P-018, P-019, P-023, P-024, P-026, P-028, P-029, P-035, P-036, P-050, P-051
- **Verified at**: 2026-09-29
- **Bài học tổng quát**: **Mọi thực thể và trạng thái dùng chung sở hữu DUY NHẤT MỘT hợp đồng kiểu dữ liệu, một canonical form và phải đồng nhất xuyên suốt mọi tầng.** Biến đổi schema trên hệ đang chạy bắt buộc qua Expand-Contract có audit gate; shared keys/concurrency primitives tập trung tại registry; DB constraints phải fail-closed theo three-valued logic của PostgreSQL.
- **Quy tắc bất biến**:
  1. *Canonical form đồng nhất các tầng*: Case-insensitive lookup phải cùng expression invariant với index (`LOWER(code) = LOWER(:code)`) hoặc canonicalize cả write/read kèm constraint. [P-003]
  2. *Raw projection alias tường minh*: `getRawOne/getRawMany` trả alias tự sinh (`b_id`); `.select("b.id", "id")` + typed raw projection — code đọc đúng key của raw result. [P-005]
  3. *Identifier đa hình phải classify trước khi bind*: Không bind một input chưa phân loại vào các cột kiểu khác nhau (bigint vs uuid → 22P02); validate format rồi chọn duy nhất predicate tương ứng. [P-006]
  4. *UUID canonical identity trong JSONB*: Validate + normalize case ở boundary; uniqueness/lookup theo canonical identity, test mixed-case trên DB thật. [P-050]
  5. *Đơn vị tiền tệ không trộn*: Chỉ cộng/trừ/so sánh monetary sau khi normalize cùng currency; response contract expose `currency` hoặc tách breakdown; cột ledger cho charge/accounting không đổi semantic âm thầm phục vụ reporting. [P-026]
  6. *Timezone là contract*: Aggregation chuỗi thời gian dùng `AT TIME ZONE` với tz whitelist-sanitized; zero-fill đủ chuỗi ngày theo múi giờ client. [P-013]
  7. *Một key một kiểu Redis*: Counter/lockout chọn INCR-string HOẶC HINCRBY-hash, cấm trộn trên cùng key; shared keys tập trung tại `CacheKeys`, cấm inline string interpolation, magic numbers → named constants. [P-008, P-023]
  8. *Migration = file + runner registration*: Hai thay đổi một cặp có kiểm tra; review cross-check `tools/migrations/*.sql` với runner manifest; test migrate trên DB scratch. [P-007]
  9. *Replay-idempotent DDL*: `CREATE TABLE IF NOT EXISTS` không bảo vệ DDL kế tiếp — mọi `ALTER`/`CREATE INDEX` guard bằng `information_schema.columns` + scope theo schema.table; dùng history table dài hạn nhưng migration hiện có vẫn phải an toàn khi retry giữa chừng. [P-018]
  10. *Expand-Contract 3 pha + audit gate*: Expand chỉ thêm nullable + backfill trường hợp thuần; Deploy binary nhận biết cả hai dạng; Cutover fail-closed (`DO $$ ... RAISE EXCEPTION` khi còn dữ liệu lỗi) và tách khỏi default expand pipeline — không chạy nếu chưa phê duyệt. [P-019, P-029]
  11. *Typed→JSONB giữ nguyên invariant*: PostgreSQL CHECK chỉ reject khi FALSE — `jsonb_typeof(missing_key)` trả UNKNOWN vẫn PASS. Constraint mới phải fail-closed với missing key, JSON null, SQL NULL, sai kiểu, sai precision; đối chiếu NOT NULL/type/FK/unique/CHECK cũ↔mới + counterexample tối thiểu. DTO validation không thay thế DB boundary. [P-051]
  12. *Universal audit standard*: Mọi table/entity 6 trường `id` bigint PK (không expose), `id_public` uuid (public contract), `created_at`/`updated_at` timestamptz, `deleted_at` soft-delete, `metadata` jsonb. [P-010]
  13. *Discriminator invariants*: `type_account` (iq_user default / 1 iq_team duy nhất per Business / iq_crm virtual không giới hạn) — validate unique trước bind/create. [P-011]
  14. *Runtime type reflection*: Cấm inline object type ở `@Body/@Query/@Param` — type erasure làm ValidationPipe bypass hoàn toàn; class DTO + decorators bắt buộc; pagination DTO extends `PaginationDTO` chung. [P-024]
  15. *Lock cần transaction*: Mọi `setLock('pessimistic_*')` phải nằm trong transaction đang mở — `DataSource.manager` bare luôn throw `PessimisticLockTransactionRequiredError`; refactor raw→ORM phải cùng commit cập nhật integration spec (entities registration, cột mới). [P-035]
  16. *Dedicated DB không share schema*: Integration spec tự apply migration dependency chain của nó (DB riêng chỉ phản ánh spec đã chạy), bọc `SAVEPOINT` quanh statement kỳ vọng reject trong transaction. [P-036]
  17. *Deterministic execution identity*: Entity PK dẫn xuất từ queue job.id — PK database là concurrency gate cuối chống retry-duplicate (chi tiết luồng tại G-005). [P-028]
  18. *Dual-registry consistency*: Trạng thái session ghi đồng thời PostgreSQL (durable) + Redis (fast guard) với TTL khớp token; backward-compatible với token cũ thiếu field. [P-015]
- **Gợi ý học thêm**:
  1. Martin Fowler — Evolutionary Database Design & Expand-Contract Pattern
  2. PostgreSQL Transactional DDL, Indexes on Expressions, three-valued CHECK logic
  3. Money value objects & currency-aware ledgers

### [G-003] Zero-Trust Context & Scope Boundaries
- **Type**: principle
- **Status**: promotion-pending
- **Review**: needs-cross-review
- **Root cause**: trust-boundary-and-execution-scope-violation
- **Scope**: security/identity/ownership/prompt-scope/architecture-boundaries/enforcement-gates
- **Failure mechanism class**: Vi phạm ranh giới tin cậy hoặc ranh giới can thiệp: (1) chấp nhận identity/scope do client gửi thay vì principal xác thực → IDOR, leo thang; (2) prompt/task thiếu forbidden scope → implementer vượt giới hạn hoặc nhân bản nợ kỹ thuật; (3) enforcement gate có entrypoint bypass → PASS giả.
- **Covered patterns**: P-001, P-004, P-012, P-014, P-016, P-017, P-023, P-025, P-033
- **Verified at**: 2026-09-29
- **Bài học tổng quát**: **Mọi quyền hạn, phạm vi truy cập và can thiệp code phải neo vào principal xác thực và ranh giới kiến trúc rõ ràng.** Không bao giờ chấp nhận client-supplied scope cho quyết định bảo mật. Mọi prompt/task khóa đủ 3 ranh giới (allowed/forbidden/compatibility). Mọi enforcement gate phải fail-closed, không bypass flag, mọi entrypoint delegate vào một core validator duy nhất.
- **Quy tắc bất biến**:
  1. *Identity chỉ từ authenticated principal*: Cấm `userId`/`tenantId`/`refCode`/actor-id trong request DTO — trích từ `@RequestUser()`. Mọi query theo resource id phải scope `WHERE owner = principal` hoặc ownership check trước mutate. Negative test bắt buộc: user A gọi resource của B → 403/404. [P-017]
  2. *Portal controller pattern*: Controller portal gắn `@UseGuards(AuthGuard)` trong domain module — tránh guard collision mà không sinh micro-module rỗng. Financial mutation = `@IsPositive` DTO + lock balance trong cùng transaction + `@UseCallQueue(1)`. [P-017]
  3. *Hierarchy ceiling + cascade re-cap*: `ratePercent` con ≤ `myMaxRate` của caller; khi hạ trần node cha phải quét đệ quy hạ trần toàn bộ tuyến dưới vượt trần mới. Cấp trên chỉ sửa F1 trực tiếp (`targetUser.refCode === currentUser.code`). [P-014]
  4. *Domain prerequisite guards*: Invariant nghiệp vụ dạng "B phải có A đi kèm" (schedule phải có iq_team) chặn ở cả API layer (BadRequest) và producer layer (skip toàn slot, không chạy lẻ). [P-012]
  5. *3-boundary prompt lock*: Mọi prompt/task khai báo allowed files, forbidden files/actions, compatibility boundary. Multi-tenant prompt phải khai báo field ownership theo discriminator mode; cấm internal numeric ID trên public REST contract. [P-001, P-004]
  6. *Reuse owner, cấm parallel reimplementation*: Feature protocol mới mở rộng transport/session owner hiện có — chỉ tách khi chứng minh protocol/credential/lifecycle khác biệt; trace entrypoint + session owner + outbound channel + inbound dispatcher trước khi tạo client mới. [P-033]
  7. *Domain cohesion over fragmentation*: Mặc định mở rộng module hiện có theo tầng `controllers/services/entities`; cấm module bọc 1 endpoint/method; cross-domain enrichment inject trực tiếp `TypeOrmModule.forFeature([Entity])` thay vì import chéo module (circular DI); `forwardRef` là phương án cuối; đổi tên module trùng framework (`ScheduleModule` → `TradeScheduleModule`); multi-path routing bằng mảng `@Controller([...])`. [P-016]
  8. *Surgical ≠ nhân bản nợ*: Khi code cũ có hardcode/shared key inline, phần chạm tới phải chuẩn hóa về registry (`CacheKeys`) và named constants — "giữ diff nhỏ" không phải cái cớ copy technical debt. [P-023]
  9. *Enforcement gate fail-closed tuyệt đối*: Kiểm kê mọi executable entrypoint của gate; wrapper legacy phải delegate vào core validator duy nhất; cấm bypass flag ở đường CI; thiếu config/PR SHA/runner/postflight artifact → exit nonzero, cấm in PASS. [P-025]
- **Gợi ý học thêm**:
  1. OWASP Top 10 — Broken Object Level Authorization (IDOR/BOLA)
  2. Domain-Driven Design — Bounded Contexts, Aggregates & Module Cohesion
  3. Fail-closed CI gate design & evidence-based policy enforcement

### [G-004] Durable Financial State-Machine Safety
- **Type**: principle
- **Status**: promotion-pending
- **Review**: needs-cross-review
- **Root cause**: premature-terminal-transition-and-side-effect-ordering
- **Scope**: financial-mutations/state-transitions/reconciliation/idempotency/durable-ordering
- **Failure mechanism class**: Một mutation tài chính bị promote sang trạng thái terminal (SUCCEEDED/REJECTED/FAILED) từ bằng chứng chưa có thẩm quyền (chat text, socket ACK, delta hiển thị, timeout), hoặc durable writes sai thứ tự (ack trước side-effect, finalize trước ledger hội tụ). Hệ quả: fence concurrency bị dỡ → double-spend; derived mirror chốt sai balance; crash-window mất side-effect vĩnh viễn.
- **Covered patterns**: P-019, P-027, P-030, P-031, P-032, P-048
- **Verified at**: 2026-09-29
- **Bài học tổng quát**: **Provisional evidence không bao giờ promote thành terminal financial state.** Mọi mutation đã phát tán qua ranh giới mạng mà outcome chưa rõ là AMBIGUOUS — giữ fence chặn replay cho tới khi authoritative ledger/reconciler xác minh. Thứ tự durable bắt buộc: intent → dispatch → side-effect → ack.
- **Quy tắc bất biến**:
  1. *Provisional ≠ terminal*: Chat text, ACK socket, connection check, balance delta từ broker ngoài chỉ là `provisional`/`accepted`. Chỉ promote `SUCCEEDED` khi có authoritative transaction ledger correlation hoặc read-only oracle chứng thực độc lập. SDK/worker phải trả `provisional: true` cho mọi giao tiếp chat/delta. [P-031]
  2. *Reconciler cấm fake oracle*: Daemon reconcile cấm tự trả `succeeded` cho mutation (DELETE_CHAT, HIDE_POSITION...) dựa trên socket state hay evidence cũ; không có bằng chứng dứt điểm → `ambiguous`. `sentAt == null` (crash trước khi frame rời server) → cấm gọi broker/promote, chỉ `EXPIRED` sau challenge window. Reconcile quá ngưỡng (5 attempts) → `MANUAL_REVIEW`. [P-031]
  3. *Ambiguity giữ concurrency fence*: Sau khi frame đã gửi, mọi exception/timeout/disconnect phân loại `AMBIGUOUS` — KHÔNG `REJECTED`, vì terminal rejection dỡ unresolved fence (partial unique index) và mở đường replay trùng lệnh tiền thật. `REJECTED` chỉ khi broker trả rejection authoritative. [P-032]
  4. *Thứ tự durable writes*: intent → dispatch → side-effect → ack. Marker "đã xong" persist SAU side-effect commit; ack trước side-effect tạo crash-window mất side-effect vĩnh viễn (remaining tính ra rỗng → skip không self-heal). Re-drive phải tính remaining từ ground-truth của side-effect (`hidden` flag), không từ ack map. [P-048]
  5. *Terminal-status tôn trọng exclusion boundary*: Khi status enum kiêm vai trò exclusion predicate, phân loại terminal write theo "dispatch intent đã tồn tại chưa": post-dispatch error → `MANUAL_REVIEW` (boundary giữ nguyên), pre-dispatch → `FAILED`. Status "failure" làm un-exclude work item tiền thật là đường fail-open. [P-048]
  6. *Derived ledger hội tụ trước finalize*: Financial mirror là derived ledger — finalize chỉ khi source terminal + toàn bộ persisted ledger hội tụ + tuple owner (user/account/subscription/schedule) xác minh bằng cột authoritative. Không nuốt lỗi ghi mirror ở callback; không coerce missing/null/non-finite P&L thành 0; `closed` chỉ là terminal khi P&L hữu hạn hợp lệ. [P-030]
  7. *Role eligibility tại mutation owner*: Charge/debit logic kiểm domain role (`iq_user`/`iq_team`/`iq_crm`) tại service sở hữu side-effect cuối cùng — caller flag (`skipFree`) không bảo vệ được invariant tiền thật vì mỗi caller tự truyền default. Regression test cho từng role charged/exempted. [P-027]
  8. *Ledger atomicity*: Paired debit/credit trong cùng 1 transaction; deterministic `refCode` không truncate (idempotency); advisory lock cho transfer; immutable business key `UNIQUE(trade_id, trader_id, referrer_id) WHERE trade_id IS NOT NULL` xuyên status/deleted_at; cấm soft-delete entity audit; zero fake currency. [P-019]
- **Gợi ý học thêm**:
  1. Two-phase commit & provisional vs authoritative state in payment integrations
  2. Concurrency fences and in-doubt states in distributed transactions
  3. Double-entry bookkeeping & idempotent financial transfers

### [G-005] Async Concurrency & Shared-State Ownership
- **Type**: principle
- **Status**: promotion-pending
- **Review**: needs-cross-review
- **Root cause**: check-then-set-race-and-unbounded-shared-state
- **Scope**: concurrency/distributed-locks/async-entrypoints/shared-queues/worker-dispatch/idempotency
- **Failure mechanism class**: Concurrency primitive thiếu atomicity hoặc ownership: check-then-set guard cho 2 actor cùng qua; lock release không kiểm token xóa nhầm lock chủ khác; promise/callback không settle khi throw lạ; busy-poll shared state đốt CPU; shared queue giữ stale events đầu độc operation mới; worker retry sinh entity ID ngẫu nhiên nhân đôi side-effect.
- **Covered patterns**: P-009, P-012, P-028, P-043, P-044, P-045
- **Verified at**: 2026-09-29
- **Bài học tổng quát**: **Mọi concurrency guard phải atomic (CAS), mọi async entrypoint phải settle-exactly-once, mọi wait phải bounded+signaled, mọi shared queue phải drain tại operation boundary, và PK database là concurrency gate cuối cùng.** Check-then-set không phải guard.
- **Quy tắc bất biến**:
  1. *Distributed lock release atomic*: Release lock phân tán bắt buộc Compare-and-Delete Lua (`if get == token then del`), không `DEL` trần — phiên quá hạn xóa nhầm lock phiên kế tiếp. Lock 2 cấp: account-level + schedule-level; busy-check defer cron slot tiếp theo khi phiên cũ chưa xong. [P-009]
  2. *In-flight guard phải CAS*: `job?.isActive` là check-then-set — 2 request đồng thời cùng qua → 2 session song song vi phạm spec. Dùng `AtomicBoolean.compareAndSet` (hoặc tương đương); flag reset trong `finally` kể cả early-return. [P-043]
  3. *Settle exactly-once*: Mọi promise/result callback của async entrypoint phải settle đúng 1 lần kể cả khi native throw bất ngờ — `catch (e: Exception)` ngoài `catch (CancellationException)`, reject typed error, `finally` giải phóng secret + reset guard. Decode untyped map catch rộng: `!!`/`as` ném NPE/ClassCastException, không phải IllegalArgumentException. [P-043]
  4. *Wait bounded + signaled*: Cấm vòng `while` rỗng poll shared state (giữ GIL/đốt CPU) — dùng `Event.wait(timeout)` hoặc blocking queue, fallback sleep ≥10ms. Frame inbound route theo discriminator tới đúng owning handler; mỗi handler đúng 1 lần/frame. [P-044]
  5. *Shared queue drain tại op boundary*: Queue sống suốt session tích stale events của workflow cũ — drain ở đầu mỗi operation; predicate của op mới chỉ match events sinh sau khi gửi lệnh. Sau destructive op (delete/reset) cold-start chậm → retry 1 lần trước khi mark ambiguous. Chỉ áp dụng shared mode — standalone có queue riêng. [P-045]
  6. *Deterministic execution ID*: Background worker có side-effect tài chính dẫn xuất Entity ID từ `job.id` của queue — retry không sinh phiên thứ hai. Tại init boundary bắt Postgres `23505` → return idempotent + warn, không khởi động session mới. PK là atomic gate cuối. [P-028]
  7. *Fault isolation giữa lane*: Các lane dispatch độc lập (team/user job id + lock riêng); lỗi xác thực một lane không ảnh hưởng lane còn lại; lane chính vẫn chạy khi lane phụ vắng subscriber (fail-safe). [P-012]
  8. *Round-robin pointer có fallback*: Con trỏ duyệt subscription (`id > lastId`) cần fallback khi subscription bị xóa/đổi giữa chu kỳ. [P-009]
- **Gợi ý học thêm**:
  1. Distributed Locks with Redis (Compare-and-Delete Lua, fencing tokens)
  2. At-Least-Once Delivery & Exactly-Once Processing Semantics
  3. Kotlin coroutines — structured concurrency & promise settlement

### [G-006] Test Surface Must Match Runtime Contract Truth
- **Type**: principle
- **Status**: promotion-pending
- **Review**: needs-cross-review
- **Root cause**: test-fixture-and-double-mismatch-to-runtime-surface
- **Scope**: test-doubles/fixtures/environment-isolation/coverage-adequacy/verification-method
- **Failure mechanism class**: "Test xanh" không chứng minh behavior thật: mock tự suy signature/return shape khác contract; fixture không drive predicate của nhánh cần test; suite env-gated bị skip; spec thiếu schema dependency; verification gate chỉ so string/metadata. Code đúng + test pass nhưng nhánh mới chưa từng chạy trên artifact thật.
- **Covered patterns**: P-002, P-005, P-008, P-020, P-022, P-035, P-036, P-040, P-047, P-049
- **Verified at**: 2026-09-29
- **Bài học tổng quát**: **"Có test" ≠ "có coverage".** Test double phải tuân thủ runtime contract về signature, return shape và failure semantics; fixture phải chứng minh drive execution qua đúng nhánh; môi trường test phải cô lập và spec tự apply dependency chain của nó; verification method được review nghiêm như production code.
- **Quy tắc bất biến**:
  1. *Double contract-faithful*: Mock/stub phải type-check theo contract thật của base class (`Pick<Service,...>`/`satisfies`) — cấm tự suy tên method/casing. Reviewer mở base-class signature trước khi chấp nhận test double. Mock phải đủ field contract — thiếu field (vd `balance_type`) làm test xanh trong khi contract thật lệch. [P-002, P-022]
  2. *Type contract chỉ chứng minh trên instance thật*: Mock `getNumber`/`hincrby` độc lập không bao giờ ném WRONGTYPE — logic phụ thuộc Redis/DB type cần ≥1 test trên instance thật cho đường ghi/đọc/expire. Raw QueryBuilder mapping tương tự: unit mock entity không chứng minh alias. [P-008, P-005]
  3. *Fixture phải drive đúng nhánh*: Với mỗi nhánh điều kiện mới trong SQL/code (CTE leg, CASE, status-gate), seed dữ liệu làm predicate đó true và assert giá trị thật trên DB. `expect(sql).toContain(...)` chỉ chứng minh cú pháp — nhánh sai semantics vẫn pass. Review test-coverage adequacy tách khỏi implementation correctness; đặc biệt gate `status IN`, `EXISTS`, `IS DISTINCT FROM`. [P-047]
  4. *Môi trường test cô lập bắt buộc*: Integration test chạm PostgreSQL thật phải qua `loadDedicatedPostgresTestConfig()` — `DB_INTEGRATION_DATABASE !== DB_DATABASE`, suffix `_test/_integration/_scratch`, fail-fast `beforeAll`. Cấm chạy trên database dev/app. [P-020]
  5. *Spec tự apply dependency chain*: Dedicated DB chỉ phản ánh spec đã chạy — spec cho migration N phải apply mọi migration production code đụng tới (kể cả read-only query) theo thứ tự manifest trong `beforeAll`. Insert kỳ vọng reject trong transaction dài phải bọc `SAVEPOINT`/`ROLLBACK TO SAVEPOINT`. [P-036]
  6. *Env-gated suite phải thật sự chạy*: Suite skip theo env (`RUN_DB_INTEGRATION`) để lỗi runtime (lock ngoài transaction, entity chưa đăng ký) sống sót trong code đã commit — refactor đổi contract bắt buộc chạy integration trên DB riêng trước khi commit. Commit refactor phải kèm cập nhật spec — nếu không suite fail vì lỗi spec và che mất lỗi production. [P-035]
  7. *Verification method được review như code*: Gate chỉ so string/metadata không chứng minh schema/infra behavior → đối chứng bằng integration test trên artifact thật. Với mỗi test xanh hỏi ở method-level: test này chứng minh behavior gì, hay chỉ chứng minh artifact tồn tại? [P-049]
  8. *Fix theo invariant — enumerate mọi site*: Viện dẫn một invariant phải liệt kê TẤT CẢ code path chạm resource đó (Redis I/O, provider thread, shared list) — fix instance đầu tiên là nửa fix. Mọi fact bất thường trong scout phải xuất hiện trong report: finding hoặc rejected candidate kèm lý do — cấm "biết nhưng bỏ qua ngầm". [P-049]
  9. *Severity = blast radius × likelihood*: Không gán severity theo "có vi phạm spec literal không". Unbounded growth trên shared-infra nuôi trading path tối thiểu MAJOR. Hỏi: ai chung resource? ai đọc data này? crash giữa chừng thì sao? — trước khi gán MINOR. [P-049]
  10. *Device/JVM divergence*: Provider shadow, provider bị strip, behavior khác giữa JVM test và device — bug chỉ lộ ở instrumentation/device test; unit test xanh không loại trừ. [P-040]
- **Gợi ý học thêm**:
  1. Martin Fowler — Mocks Aren't Stubs & Database Integration Testing Patterns
  2. Coverage adequacy vs test existence (mutation testing mindset)
  3. Ephemeral test environments & test-database isolation

---

## Patterns

> Stub layer: mỗi pattern giữ heading + `Status` + `Bài học`/`Phòng ngừa` 1 dòng
> + con trỏ evidence. Evidence đầy đủ (occurrences, file:line, verification)
> nằm trong `learning-metadata.json`; bản text gốc nằm trong
> `learning-patterns-archive-20260929.md`.

### [P-001] Scope Lock & Contract Boundary cho Auth Versioning Docs
- **Status**: active
- **Bài học**: Prompt docs-only phải khóa Forbidden Actions, backward compatibility và deliverable file để Agent không tự mở rộng scope. → [G-003]
- **Phòng ngừa**: Reviewer kiểm tra ba contract — allowed files, forbidden files, compatibility boundary — trước khi giao prompt.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-08-24 · trading-accounts-register)

### [P-002] Mock Masking Method Casing Mismatch in Inherited Base Classes
- **Status**: active
- **Bài học**: Mock dependency phải type-check theo contract thật của base class; mock thủ công không tự suy tên method. → [G-001, G-006]
- **Phòng ngừa**: Dùng typed mock (`Pick`/`satisfies`); reviewer mở base-class signature trước khi chấp nhận test double.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-08-24 · trading-accounts-register)

### [P-003] Case-insensitive Query Does Not Match LOWER Expression Index
- **Status**: active
- **Bài học**: Case-insensitive lookup phải dùng `LOWER(code) = LOWER(:code)` khớp expression index, hoặc canonicalize cả write/read. → [G-002]
- **Phòng ngừa**: Integration test với stored/input casing khác nhau; kiểm tra query expression/schema invariant, không chỉ unit mock.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-08-24 · trading-accounts-register)

### [P-004] Multi-tenant Discriminator Metadata Prompt Boundary
- **Status**: active
- **Bài học**: Prompt multi-tenant phải chỉ rõ aggregate sở hữu từng metadata field theo mode và cấm internal ID trên public contracts. → [G-003]
- **Phòng ngừa**: Bảng field ownership theo discriminator mode + contract test yêu cầu `id_public`, loại internal `id`.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-08-25 · business-module-schedule-refactor)

### [P-005] TypeORM Raw Query Column Alias Mismatch
- **Status**: active
- **Bài học**: Raw QueryBuilder phải khai báo alias tường minh (`.select("b.id", "id")`); code đọc đúng key raw result — unit mock không chứng minh mapping. → [G-001, G-002, G-006]
- **Phòng ngừa**: Typed raw projection + integration test với QueryBuilder thật.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-08-25 · business-config)

### [P-006] PostgreSQL Heterogeneous Parameter Binding
- **Status**: active
- **Bài học**: Identifier đa hình phải classify trước khi dựng predicate; không bind cùng input vào cột kiểu khác nhau (bigint vs uuid). → [G-002]
- **Phòng ngừa**: Validate ID format rồi chọn duy nhất predicate; integration test cho numeric/UUID/malformed.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-08-25, 2026-09-03 · schedule-executor, service-deployment)

### [P-007] Migration File Present But Not Registered In Runner
- **Status**: active
- **Bài học**: Migration mới = 2 thay đổi một cặp: file SQL + đăng ký runner; review cross-check manifest với file tồn tại. → [G-001, G-002]
- **Phòng ngừa**: Script check `tools/migrations/*.sql` vs `FILES`; integration test chạy `npm run migrate` trên DB scratch.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-08-26 · bot-trade-nestjs PR #7)

### [P-008] Redis Hash/String Type Mixing On Counter Key
- **Status**: active
- **Bài học**: Counter/lockout Redis chọn MỘT kiểu (INCR-string hoặc HINCRBY-hash); mock che giấu WRONGTYPE — cần test instance thật. → [G-002, G-006]
- **Phòng ngừa**: Quy ước attempt/lockout counter dùng INCR + EXPIRE; integration test Redis thật khi logic phụ thuộc kiểu.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-08-26 · bot-trade-nestjs PR #7)

### [P-009] Sequential Hourly Schedule Dispatch with Busy Deferral & Atomic CAD Locks
- **Status**: active
- **Bài học**: Điều phối tuần tự cần round-robin pointer có fallback + lock 2 cấp (account/schedule) + busy-check defer + release bằng Lua Compare-and-Delete. → [G-005]
- **Phòng ngừa**: Mẫu Round-Robin + Sweep mutex 30s + Busy check + CAD Lua release cho mọi scheduler/executor tự động.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-08-27 · bot-trade-nestjs)

### [P-010] Universal Table Architecture Standard (id, id_public, created_at, updated_at, deleted_at, metadata)
- **Status**: active
- **Bài học**: MỌI table/entity bắt buộc 6 trường chuẩn: `id` bigint PK nội bộ, `id_public` uuid public, `created_at`/`updated_at` timestamptz, `deleted_at` soft-delete, `metadata` jsonb; entity extends `AuditWithTimezone`. → [G-002]
- **Phòng ngừa**: Kiểm tra 6 trường chuẩn khi tạo migration SQL hoặc khai báo TypeORM Entity.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-08-27 · bot-trade-nestjs)

### [P-011] Trading Account Classification: type_account (iq_user, iq_team, iq_crm) & Business Invariants
- **Status**: active
- **Bài học**: `trading_accounts` dùng `type_account` (bỏ `play_mode`): `iq_user` default cho end-user, `iq_team` duy nhất 1 tài khoản/Business, `iq_crm` ảo random không giới hạn. → [G-002]
- **Phòng ngừa**: Validate quy tắc 1 `iq_team`/Business và gán default `iq_user` mọi luồng đăng ký end-user.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-08-27 · bot-trade-nestjs)

### [P-012] Dual Execution Architecture: Team-User Concurrent Dispatch & Fault Isolation
- **Status**: active
- **Bài học**: Dual dispatch 2 job độc lập (team + user round-robin) với lock riêng và fault isolation; tài khoản thứ 2 BẮT BUỘC có `iq_team` đi kèm — thiếu thì API chặn + producer skip toàn slot. → [G-003, G-005]
- **Phòng ngừa**: Producer tạo job Team/User lock riêng; test chứng minh lỗi một job không chặn job kia và không bao giờ chạy subscriber đơn lẻ.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-08-27 · bot-trade-nestjs)

### [P-013] Time-Series Profit Aggregation: Timezone Normalization, Anti-IDOR & Zero-Filling Gaps
- **Status**: active
- **Bài học**: Chart P&L phải group theo múi giờ client (`AT TIME ZONE` + tz whitelist sanitize), zero-fill đủ chuỗi ngày, và scope query bằng `user_id` từ JWT. → [G-002, G-003]
- **Phòng ngừa**: Sanitize tz + aggregation theo timezone + generator zero-filling cho mọi API time-series.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-08-28 · bot-trade-nestjs)

### [P-014] Multi-tenant Downline Commission Hierarchy & Cascade Ceiling Enforcement
- **Status**: active
- **Bài học**: Trần hoa hồng cấp trên (`ratePercent <= myMaxRate`) + cascade re-cap đệ quy khi cha bị giảm; cấp trên chỉ sửa F1 trực tiếp. → [G-003]
- **Phòng ngừa**: Kiểm tra trần cấp trên + duyệt đệ quy cascade mọi thay đổi tỷ lệ chia sẻ tài chính trong mô hình phân cấp.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-08-28 · bot-trade-nestjs)

### [P-015] Real-Time JWT Session Tracking & Instant Revocation via Fast Redis Registry
- **Status**: active
- **Bài học**: JWT stateless cần dual-layer registry — PostgreSQL durable + Redis `session:active:<id>` TTL khớp token; guard check Redis cho revocation tức thì; bypass an toàn cho JWT cũ thiếu `sessionId`. → [G-002]
- **Phòng ngừa**: JWT mới gắn `sessionId`; trạng thái phiên đồng thời PostgreSQL + Redis TTL khớp token; test thu hồi tức thì + token cũ.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-08-28 · bot-trade-nestjs)

### [P-016] Domain Module Consolidation & Anti-Circular Dependency Architecture (KISS/DRY)
- **Status**: active
- **Bài học**: Gộp domain cohesion cao về 1 module; cross-domain enrichment inject `TypeOrmModule.forFeature([Entity])` thay vì import chéo module (circular DI); cấm module bọc 1 endpoint; đổi tên trùng framework; multi-path routing bằng mảng `@Controller`. → [G-003]
- **Phòng ngừa**: Mở rộng module hiện có theo tầng; cấm import chéo cha-con; review đếm số module mới/PR (cảnh báo >1) + kiểm DI graph không vòng.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-08-28, 2026-09-04 · bot-trade-nestjs)

### [P-017] Anti-IDOR: Client-Controlled Identity & Resource Access Must Bind to Authenticated Principal
- **Status**: active
- **Bài học**: Cấm actor-id từ client (body/query/path) — identity chỉ từ `@RequestUser()`; mọi truy vấn resource phải scope `WHERE owner = principal`; portal controller + `@IsPositive` + lock+transaction cho financial mutation. → [G-003]
- **Phòng ngừa**: Grep DTO có identity field; kiểm mọi `:id` endpoint có scope theo principal; test âm A→B = 403/404.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-08-30 · bot-trade-nestjs)

### [P-018] Legacy Migration Replay Against A Newer Table Shape
- **Status**: active
- **Bài học**: Migration replay phải idempotent theo cả object-existence và shape hiện tại; `CREATE TABLE IF NOT EXISTS` không bảo vệ `ALTER`/`CREATE INDEX` kế tiếp tham chiếu cột đã đổi. → [G-002]
- **Phòng ngừa**: Guard DDL bằng `information_schema.columns` + scope constraint/index theo schema.table; regression test replay migration cũ trên terminal schema.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-08-31 · bot-trade-nestjs)

### [P-019] Multi-Wallet Schema Rollout: Expand-Contract 2-Step Cutover & Financial Ledger Atomicity
- **Status**: active
- **Bài học**: Tách ví trên hệ đang chạy: Expand (nullable + unique index bất biến) → Deploy code nhận biết → Cutover PK; zero fake currency; immutable business key xuyên status/deleted_at; deterministic refCode không truncate + paired ledger trong 1 transaction + advisory lock. → [G-002, G-004]
- **Phòng ngừa**: Kiểm tương thích 2 chiều trong migration; integration test concurrency + replay trước khi bật flag.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-09-03 · bot-trade-nestjs)

### [P-020] Dedicated Database Isolation & Safety Guardrails for Live Integration Testing
- **Status**: active
- **Bài học**: Integration test chạm PostgreSQL thật bắt buộc `loadDedicatedPostgresTestConfig()` — DB riêng (suffix `_test/_integration/_scratch`), cấm `DB_DATABASE`; enforce runtime contract truth thay mock sai. → [G-001, G-006]
- **Phòng ngừa**: `loadDedicatedPostgresTestConfig` làm chuẩn chung; fail-fast `beforeAll` trước mọi connection.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-09-03, 2026-09-04 · bot-trade-nestjs)

### [P-021] Runtime Prerequisite & Business Semantics Verification Before Deductive Reasoning
- **Status**: active
- **Bài học**: Cấm suy đoán xuôi dòng — kết luận side-effect chỉ sau khi trace entrypoint và chứng minh mọi precondition (auth, lock, balance, entity tồn tại); thực thể ảo/mock/CRM không xác thực được hệ thống thật. → [G-001]
- **Phòng ngừa**: Khi phân tích flow, kiểm tra 2 chiều: bản chất thực thể dữ liệu + toàn bộ chuỗi runtime preconditions.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-09-04 · bot-trade-nestjs)

### [P-022] Broker Balance Mode Must Be Verified Before Labeling or Trading
- **Status**: active
- **Bài học**: Số dư broker chỉ được gắn nhãn/cache/dùng làm điều kiện giao dịch sau khi response xác nhận `ok` + amount hữu hạn + mode thực tế trùng yêu cầu; DB preference không phải runtime state. → [G-001]
- **Phòng ngừa**: Discriminated typed contract; không nuốt lỗi chuyển wallet; fail closed tại financial gateway; test các negative path với response shape thật.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-09-06 · bot-trade-nestjs)
  - **Occurrence 2 (2026-10-29 · gsw-nx-monorepo falconati-api PR #97 review)**: Tài khoản CRM (iq_crm) tự gán status 'valid' khi chưa verify broker credentials/balance qua gateway sàn. Fix: gán 'unknown' khi thiếu kiểm chứng runtime, cấm assume valid từ DB preference.

### [P-023] Blind Local Pattern Mimicry & Plan-Scope Compliance Traps
- **Status**: active
- **Bài học**: "Surgical changes" không đồng nghĩa sao chép technical debt — khi mở rộng logic có hardcode/shared key phải chuẩn hóa về `CacheKeys` + named constants; phạm vi plan là ranh giới kiến trúc, không phải cái cớ vi phạm DRY. → [G-003]
- **Phòng ngừa**: Cấm inline string interpolation cho shared state; magic numbers → named constants; refactor tối thiểu phần chạm tới.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-09-07 · bot-trade-nestjs)

### [P-024] Runtime Validation Bypass via Anonymous Inline Controller Types & Ad-hoc Pagination DTOs
- **Status**: active
- **Bài học**: Inline TypeScript object type ở `@Body/@Query/@Param` bị type erasure → ValidationPipe bypass hoàn toàn; bắt buộc class DTO + decorators; pagination extends `PaginationDTO` chung. → [G-002]
- **Phòng ngừa**: Cấm `@Body() body: { ... }`/`@Query() query: { ... }` mọi controller; class DTO trong `dto/` với class-validator + `@ApiProperty`.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-09-07 · bot-trade-nestjs)

### [P-025] Legacy CI Wrapper Bypass Must Fail Closed
- **Status**: active
- **Bài học**: CI enforcement chỉ đáng tin khi mọi entrypoint delegate vào một core contract duy nhất và fail closed; wrapper tự copy flow giữ bypass flag + PASS giả làm gate mất chặn. → [G-003]
- **Phòng ngừa**: Kiểm kê mọi executable entrypoint; regression test cho wrapper; loại bypass flag; thiếu config/SHA/runner/postflight → nonzero, cấm in PASS.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-09-09 · codex-global-governance-hardening)

### [P-026] Mixed Monetary Units in Aggregated Financial API
- **Status**: active
- **Bài học**: Financial API chỉ cộng/trừ/so sánh monetary sau khi normalize cùng currency + expose `currency` trong contract; cột ledger không đổi semantic âm thầm phục vụ reporting. → [G-002]
- **Phòng ngừa**: Kiểm entity xác định currency từng cột; normalize bằng tỷ giá owner có sẵn trước aggregation; regression test mixed USD/VND.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-09-09 · bot-trade-nestjs)

### [P-027] Account Role Fee Exemption Missing From Financial Finalization
- **Status**: active
- **Bài học**: Charge logic phải xét domain role (`iq_user`/`iq_team`/`iq_crm`) tại service sở hữu debit cuối — caller flag `skipFree` không thay invariant khi mỗi caller tự truyền default. → [G-004]
- **Phòng ngừa**: Trace toàn bộ caller tạo trade + account role; eligibility đặt tại mutation owner; regression test từng role charged/exempted.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-09-09 · bot-trade-nestjs)

### [P-028] Deterministic Queue Execution ID as Database Concurrency Barrier against Retry Duplication
- **Status**: active
- **Bài học**: Worker có side-effect tài chính dẫn xuất Entity ID từ `job.id`; PK database là atomic concurrency gate cuối — bắt `23505` → return idempotent thay vì mở phiên thứ hai. → [G-002, G-005]
- **Phòng ngừa**: Map `job.id` → `executionId` truyền xuống runner; `executionId` làm PK/unique composite; catch `23505` tại init boundary.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-09-09 · bot-trade-nestjs)

### [P-029] Staged Discriminator Rollout with Fail-Closed Audit Gate
- **Status**: active
- **Bài học**: Discriminator rollout trên bảng cha-con có legacy data: tách expand/cutover — expand chỉ nullable + backfill thuần; cutover giữ khỏi default pipeline, chỉ chạy khi chỉ định tường minh, assertion `RAISE EXCEPTION` khi còn dữ liệu lỗi. → [G-002]
- **Phòng ngừa**: 2 file migration riêng; cutover ngoài `EXPAND_MIGRATIONS`; assertion 100% rows hợp lệ trước NOT NULL/composite FK; live test cả 3 nhánh fail-closed.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-09-09 · bot-trade-nestjs)

### [P-030] Derived Financial Mirror Must Finalize Only From a Complete Persisted Ledger
- **Status**: active
- **Bài học**: Mirror là derived ledger — finalize chỉ khi source terminal + persisted ledger hội tụ + tuple owner xác minh; không nuốt lỗi ghi mirror; không coerce P&L missing thành 0; `closed` chỉ terminal khi P&L hữu hạn. → [G-004]
- **Phòng ngừa**: Tách broker execution khỏi mirror finalization; deterministic row identity sync idempotent; negative test cho sai tuple/missing/non-finite P&L/retry sau finalize.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-09-10 · bot-trade-nestjs)

### [P-031] Provisional Broker Communication Must Never Be Promoted to Terminal Financial Succeeded Without Authoritative Ledger Correlation
- **Status**: active
- **Bài học**: Chat/ACK/delta chỉ là provisional — SUCCEEDED chỉ khi authoritative ledger correlation; reconciler cấm fake oracle; `sentAt=null` → chỉ EXPIRED sau window; reconcile quá ngưỡng → MANUAL_REVIEW. → [G-004]
- **Phòng ngừa**: SDK/worker trả `provisional` cho chat/delta; service giữ AMBIGUOUS + lên lịch reconcile; daemon cấm `succeeded` từ evidence provisional.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-09-10 · bot-trade-nestjs)

### [P-032] Unresolved Financial Mutation Must Never Transition to REJECTED on Transport Uncertainty
- **Status**: active
- **Bài học**: Sau khi frame gửi, mọi lỗi không xác định là AMBIGUOUS — `REJECTED` dỡ unresolved fence mở replay trùng; chỉ REJECTED khi broker rejection authoritative. → [G-004]
- **Phòng ngừa**: Exception/timeout post-dispatch → `AmbiguousOutcomeError`/AMBIGUOUS; giữ partial unique index fence tới khi reconciler xác minh.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-09-10 · bot-trade-nestjs)
  - **Occurrence 2 (2026-10-29 · gsw-nx-monorepo falconati-api PR #97 review)**: Lỗi broker session/timeout khi reconcile bị suy diễn thành 'lệnh không tồn tại' → force-close lệnh mở thành failed/timed_out với profit=0. Fix: brokerHistoryFetched guard abort reconcile khi broker unavailable, cấm force-close khi thiếu bằng chứng sàn.

### [P-033] Protocol Features Must Reuse the Existing Transport Owner
- **Status**: active
- **Bài học**: Feature protocol mới mở rộng transport/session owner hiện có, giữ feature state ở domain layer — cấm tạo transport/parser/dispatch song song; chỉ tách khi chứng minh protocol/credential/lifecycle khác. → [G-003]
- **Phòng ngừa**: Trace entrypoint + session owner + outbound channel + inbound dispatcher trước khi tạo client mới; reuse authenticated session khi scope 1 session/account.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-09-11 · bot-trade-nestjs)

### [P-034] Contract Claims in Long Plans Must Be Re-Verified at Adjudication Time — Parallel Sessions Mutate Ground Truth
- **Status**: active
- **Bài học**: Contract claim quyết định architecture phải re-verify ngay trước finalize — parallel session có thể mutate giữa read và plan; `git status`/`git log` quyết định snapshot mới hơn, không tranh luận giữa 2 snapshot. → [G-001]
- **Phòng ngừa**: Trước adjudication re-`git status` + `git log -3` trên file chứa premise-critical contract; reviewer cite file:line mâu thuẫn → re-read trực tiếp.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-09-11 · bot-trade-nestjs)

### [P-035] Pessimistic Lock on Bare EntityManager Always Throws — ORM-Refactor Validation Gap
- **Status**: active
- **Bài học**: `setLock('pessimistic_*')` bắt buộc trong transaction mở — bare `DataSource.manager` luôn throw; refactor raw→ORM phải cùng commit cập nhật integration spec (entities, cột mới) nếu không suite fail vì lỗi spec che lỗi production. → [G-002, G-006]
- **Phòng ngừa**: Grep `setLock('pessimistic` khi review; mỗi hit có đường truyền từ `dataSource.transaction()`/`QueryRunner`; env-gated suite chạy trên DB riêng trước commit.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-09-11 · bot-trade-nestjs)
  - **Occurrence 2 (2026-10-29 · gsw-nx-monorepo falconati-api PR #97 review)**: Raw query `SELECT ... FOR UPDATE` gọi qua `dataSource.query()` ngoài explicit transaction chạy autocommit làm nhả row lock ngay lập tức, vô hiệu hóa concurrency control. Fix: bọc toàn bộ trong `dataSource.transaction()` hoặc `QueryRunner`.

### [P-036] Dedicated Integration DB Can Lag the Migration Manifest — Specs Must Apply Their Own Dependency Chain
- **Status**: active
- **Bài học**: Spec cho migration N tự apply dependency chain (hoặc object tối thiểu) trong `beforeAll` — dedicated DB chỉ phản ánh spec đã chạy; expected-reject trong transaction dài bọc `SAVEPOINT`. → [G-002, G-006]
- **Phòng ngừa**: Apply mọi migration production code đụng tới theo thứ tự manifest; `SAVEPOINT`/`ROLLBACK TO SAVEPOINT` quanh assert `rejects.toThrow` trong `BEGIN`.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-09-11 · bot-trade-nestjs)

### [P-037] Visible Document Identifier Differs From Chip-Provisioned Credential Seed — Normalize at Adapter Layer
- **Status**: active
- **Bài học**: Định danh nhìn thấy (printed ID) ≠ credential seed trong chip — normalize ở adapter layer theo card-family; giữ frozen wire contract của SDK. → [G-001]
- **Phòng ngừa**: Khi access-control reject dù crypto đúng, nghi vấn đầu tiên là dạng credential (độ dài/padding/truncation theo card-family), không phải KDF.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-09-12 · eid-sdk)

### [P-038] Closed Algorithm Allowlist Validated Only on Spec Fixtures Rejects Real-World Signature OIDs
- **Status**: active
- **Bài học**: Allowlist crypto "closed by design" chỉ an toàn khi validate trên artifact thật của từng issuer — fixture spec không bao phủ lựa chọn quốc gia; giữ diagnostics in OID thực nhận trước reject. → [G-001]
- **Phòng ngừa**: In OID/params thực nhận trước khi reject; khi mở rộng table giữ ràng buộc phụ (digest-match, reject SHA-1/MD5).
- **Nguồn kiểm chứng**: metadata.json + archive (2026-09-14 · eid-sdk)

### [P-039] Strict DER-Shape Parser Breaks on Real-Card Encoding Quirks (Wrapper Tags, SEQUENCE-as-SET)
- **Status**: active
- **Bài học**: Parser trên dữ liệu thẻ thật phải khoan dung có chọn lọc tại encode quirks phổ biến (wrapper tag 0x77, SEQUENCE↔SET) nhưng typed-reject phần còn lại; reject reason đủ chi tiết phân biệt format vs crypto failure. → [G-001]
- **Phòng ngừa**: Xử lý application-tag wrapper + chấp nhận SET/SEQUENCE hoán đổi cho OF-constructs; reject reason enum đầy đủ trong log.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-09-14 · eid-sdk)

### [P-040] Android "BC" Provider Name Shadowed by Stripped System Provider — Use Provider Instances
- **Status**: active
- **Bài học**: Trên Android không tin provider-name lookup cho BC (bị shadow) — truyền provider instance; bug loại này chỉ lộ trên device, JVM test xanh không chứng minh. → [G-001, G-006]
- **Phòng ngừa**: Cấm `getInstance(alg, "BC")`/`setProvider("BC")` trong code chạy Android — luôn truyền instance; kiểm unit test che giấu bằng device test.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-09-14 · eid-sdk)

### [P-041] Spec-Mandated EF Assumed Present Breaks Real Cards — Fall Back to Verified Alternative Source
- **Status**: active
- **Bài học**: File spec-mandatory vẫn có thể vắng trên thẻ thật — fail-closed chỉ khi không còn nguồn chứng minh nào; verified artifact (SOD đã verify chữ ký) là fallback hợp lệ thay vì cứ thiếu file là fail. → [G-001]
- **Phòng ngừa**: Trace từng file đọc: cái nào cần cho security model vs chỉ là aid; fallback ràng nguồn có bằng chứng mật mã.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-09-12 · eid-sdk)

### [P-042] RN ReadableMap Coerces Every Number to Double — Kotlin `when`/`as Int` Dispatch Breaks Silently
- **Status**: active
- **Bài học**: RN bridge coerce mọi số thành `Double` — `when`/`as Int` trên `Number` break silently; coerce `.toInt()`/`.toLong()` trước dispatch; test feed đúng kiểu `Double` thật. → [G-001]
- **Phòng ngừa**: Test bridge mappers với `Double` literals cho mọi field số; chiều ngược encode số nguyên lớn dạng `Double`/`String`.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-09-08 · eid-sdk)

### [P-043] Async Bridge Entrypoint: Settle-Exactly-Once + CAS In-Flight Guard + Broad Untyped-Map Catch
- **Status**: active
- **Bài học**: Async entrypoint chứng minh 3 invariant: settle đúng 1 lần kể cả throw lạ (catch rộng + reject typed + finally reset), in-flight guard CAS không check-then-set, decode untyped map catch `Exception` vì `!!`/`as` không ném IllegalArgument. → [G-005]
- **Phòng ngừa**: `catch (e: Exception)` ngoài `catch (CancellationException)`; guard `AtomicBoolean.compareAndSet` reset trong `finally`; catch rộng/safe-cast untyped map.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-09-15 · eid-sdk)

### [P-044] WebSocket State Polling Must Block on Bounded Signals and Dispatch Only to the Owning Handler
- **Status**: active
- **Bài học**: Chờ state của WebSocket thread bằng `Event.wait(timeout)`/blocking queue — cấm busy-poll (đốt CPU); frame route theo discriminator tới đúng owner handler, mỗi handler 1 lần/frame. → [G-005]
- **Phòng ngừa**: Polling loop có deadline + wait có timeout, fallback sleep ≥10ms; dispatch map theo `message["name"]`; regression test handler không liên quan không được gọi + mỗi handler 1 lần.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-09-15 · bot-trade-nestjs)

### [P-045] Shared Event Queue Phải Drain Trước Mỗi Operation — Stale Events Đầu Độc Workflow Mới
- **Status**: active
- **Bài học**: Shared queue drain ở ranh giới mỗi operation; predicate op mới chỉ match events sinh sau khi gửi lệnh; sau destructive op cold-start chậm → retry 1 lần trước khi ambiguous. Chỉ áp dụng shared mode. → [G-005]
- **Phòng ngừa**: Drain shared + local queue đầu prepare/confirm; timeout → drain + gửi lại 1 lần; regression test seed queue events cũ chứng minh vẫn match frame thật.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-09-15 · bot-trade-nestjs)

### [P-046] Wire Format Batch Phải Verify Trên Sàn Thật — Comma vs Space Cho Kết Quả Ngược Nhau
- **Status**: active
- **Bài học**: Giữ canonical format ở storage/validation, convert wire format đúng 1 chỗ trước gửi; echo-verify toàn bộ batch parse về canonical — không check phần tử đầu; confirm batch 1 lần, timeout/mismatch → AMBIGUOUS fail-closed. → [G-001]
- **Phòng ngừa**: Test trực tiếp cả 2 format trên sàn và ghi lại broker echo; validator strict canonical ở boundary.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-09-15 · bot-trade-nestjs)

### [P-047] Fixture Không Thỏa Predicate — Test Xanh Nhưng Nhánh SQL Chưa Từng Chạy
- **Status**: active
- **Bài học**: "Có test" ≠ "có coverage" — fixture phải làm predicate từng nhánh điều kiện true; assert SQL text chỉ chứng minh cú pháp. → [G-006]
- **Phòng ngừa**: Mỗi nhánh điều kiện mới có integration test seed làm predicate true + assert giá trị thật; hỏi "fixture có drive qua nhánh này không" cho mọi test mới.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-09-17, 2026-09-24 · bot-trade-nestjs)

### [P-048] Terminal-Status Write Đổi Exclusion Boundary + Ack-Trước-Side-Effect — Fail-Open Tiền Thật Sau Crash
- **Status**: active
- **Bài học**: Terminal write phân loại theo dispatch-intent: post-dispatch error → MANUAL_REVIEW (giữ exclusion boundary), pre-dispatch → FAILED; marker "đã xong" persist SAU side-effect commit — thứ tự durable: intent → dispatch → side-effect → ack. → [G-004]
- **Phòng ngừa**: Liệt kê query predicate đọc status khi thêm terminal nhánh; re-drive tính remaining từ ground-truth side-effect; test cặp (dispatch-marker đã ghi + downstream throw).
- **Nguồn kiểm chứng**: metadata.json + archive (2026-09-23 · bot-trade-nestjs)

### [P-049] Self-Review Anchor Vào Spec-Literal — Fact Trong Tay Nhưng Không Synthesize Thành Finding
- **Status**: active
- **Bài học**: Self-review phải hỏi "chỗ nào leak/hang/grow trong production" thay "đúng spec chưa": viện dẫn invariant = enumerate mọi site; mọi fact bất thường adjudicated explicit; severity = blast radius × likelihood; verification method review như code. → [G-006]
- **Phòng ngừa**: Fix theo invariant — tick từng path chạm resource; mọi fact scout có trong report (finding hoặc rejected kèm lý do); hỏi method-level test chứng minh gì.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-09-24 · bot-trade-nestjs)

### [P-050] UUID chuyển sang JSONB phải giữ canonical identity
- **Status**: active
- **Bài học**: UUID validation phải normalize case; chuyển UUID/FK sang JSONB text có thể sai reference mapping và uniqueness — lưu canonical từ lookup có scope. → [G-002]
- **Phòng ngừa**: Kiểm chứng unique index theo identity + test mixed-case create/update/response trên DB riêng.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-09-25 · bot-trade-nestjs)

### [P-051] Typed-to-JSONB Constraint Weakening via SQL CHECK UNKNOWN
- **Status**: active
- **Bài học**: PostgreSQL CHECK chỉ reject FALSE — `jsonb_typeof(missing_key)` trả UNKNOWN vẫn pass; typed→JSONB phải giữ invariant cũ và fail-closed với missing/null/wrong-type/precision; DTO không thay DB boundary; mock repository không chứng minh CHECK/FK/index. → [G-002]
- **Phòng ngừa**: Đối chiếu nullability/type/precision/FK/unique/CHECK cũ↔mới; counterexample tối thiểu; đánh giá TRUE/FALSE/UNKNOWN; integration test missing/null/wrong-type/duplicate trên DB riêng; trace raw SQL/worker/migration writer.
- **Nguồn kiểm chứng**: metadata.json + archive (2026-09-26 · bot-trade-nestjs)

### [P-052] TypeORM `findOneBy({field: undefined})` Drops Predicate — Unbounded Match on Missing Key
- **Status**: active
- **Bài học**: Trong TypeORM 0.3.x, giá trị `undefined` trong `FindOptionsWhere` bị bỏ qua (không phải IS NULL) — `findOneBy({idPublic: undefined})` trở thành match không ràng buộc và trả row đầu tiên. Service layer nhận field đã qua DTO validation vẫn phải fail-closed khi key bắt buộc vắng, vì caller internal/future bypass ValidationPipe sẽ bind vào record tùy ý. → [G-001, G-003]
- **Phòng ngừa**: Guard `if (!key) throw` trước mọi `findOneBy*`/`findOne` nhận key từ input; không dùng `as string` để qua compiler khi field optional; test bypass-DTO assert lookup không được gọi.
- **Nguồn kiểm chứng**: review fix 2026-10-02 · bot-trade-nestjs (`subscribePassive` guard + spec 'fails closed when scheduleId is absent')

### [P-053] Falsy-Sentinel Bị Default-Normalization Nuốt — Special Behavior Cần Explicit Flag End-to-End
- **Status**: active
- **Bài học**: Khi một field có default normalization (`x is None or x <= 0 → x = 20`), truyền sentinel falsy (0/None/-1) để "xin behavior khác" không hoạt động — sentinel bị normalize ngược về default. Special-mode requests phải đi bằng flag riêng (`enter_now: bool`) map tường minh qua mọi transport boundary (params type → JSON payload → daemon kwargs → engine signature), thay vì encode vào field hiện có. → [G-001]
- **Phòng ngừa**: Trước khi design "truyền giá trị đặc biệt", đọc normalization/default block của callee; nếu falsy bị nuốt thì thêm boolean flag + giữ giá trị cũ làm graceful-degradation fallback cho stale consumers.
- **Nguồn kiểm chứng**: scout 2026-10-15 · gsw-nx falconati-nestjs (`engine.py` L1220 `enter_second` default-to-20; 3 wait sites gated `> 0`; daemon L316 explicit kwarg map)

### [P-054] Re-Point Beneficiary Phải Dedup Theo Logical Record — Beneficiary-Keyed Dedup Mint Duplicate Payout Khi Replay
- **Status**: active
- **Bài học**: Khi đổi người nhận của một financial row (fee residual platform→business), dedup keyed theo beneficiary id (`referrerId`) bị bypass vì id mới khác id cũ — replay mint row thứ hai cho cùng logical record. Idempotency key phải bind vào logical record (source entity + row role/type), không bind vào beneficiary biến đổi theo version/flag. → [G-002]
- **Phòng ngừa**: Enumerate residual/payout rows theo (sourceId, type ∈ residual-types) trước khi insert; regression test replay xuyên flag boundary với row legacy tồn tại; pre-change pending rows giữ contract cũ, cấm re-point.
- **Nguồn kiểm chứng**: prompt-authoring 2026-10-15 · gsw-nx falconati-nestjs (`docs/prompts/20261015-1922-codex-cli-fee-residual-to-business.md`; trace `referral.service.ts` `enqueueCommission` dedup `existingMap` keyed `referrerId`)



### [P-055] Client-Mock Owns Trust Boundary — Prompt Backend Phải Re-Map Mọi Invariant Mock Đang Giữ
- **Status**: active
- **Bài học**: Khi viết prompt thay mock client-side bằng backend thật, mock vô tình "sở hữu" các trust boundary (đổi trạng thái escrow bằng keyword, tính phí, auto-reply đổi status). Nếu prompt chỉ mô tả feature mà không enumerate từng invariant mock đang giữ + chỉ định owner mới phía server, agent nhận prompt sẽ port nguyên trust boundary sang client hoặc bỏ sót gate. → [G-003]
- **Phòng ngừa**: Trước khi draft prompt backend, liệt kê mọi mutation mock thực hiện (status, balance, notification) và encode mỗi cái thành MUST/MUST NOT nghiệp vụ phía server (state machine 409, ledger transaction, idempotency key, loop-guard cho bridge hai chiều).
- **Nguồn kiểm chứng**: prompt-authoring 2026-10-21 · game-item-broker (`docs/prompts/20261021-1129-chatgpt-nestjs-escrow-backend.md`; trace `mockApi.updateOrderStatus` + keyword auto-reply client đổi order sang confirmed/completed)

### [P-056] Mixed-Persistence Prompt Phải Pin Data Placement — Agent Tự Quyết Vị Trí Lưu Entity Khi Contract Thiếu
- **Status**: active
- **Bài học**: Khi user yêu cầu kết hợp nhiều datastore (Postgres + Mongo) mà prompt không ghi rõ entity nào lưu ở đâu, agent nhận prompt sẽ tự phân bổ — dễ đưa money/status/ownership vào store thiếu transaction/FK. Placement contract + cross-DB rule (no FK, no cross-DB txn, service-layer parent check) phải nằm trong prompt, không để model suy diễn. → [G-002]
- **Phòng ngừa**: Bảng placement tường minh trong prompt + rule "money/status/ownership chỉ ở store ACID" + stop condition "ask before moving any entity between stores".
- **Nguồn kiểm chứng**: prompt-authoring 2026-10-21 · game-item-broker (`docs/prompts/20261021-1129-chatgpt-nestjs-escrow-backend.md` rev 1133; user yêu cầu users/system_users tách bảng + dual Postgres/Mongo)

### [P-057] Webhook Dedup Theo Business-Ref Thay Vì Provider-Tx-Id — Tiền Thật Rơi Khỏi Ledger
- **Status**: active
- **Bài học**: Idempotency của payment webhook phải key theo `provider_tx_id`, không phải business ref code. Khi handler return sớm vì entity ở terminal state (deposit completed / order không còn pending), một giao dịch ngân hàng THỨ HAI dùng lại ref bị nuốt hoàn toàn — không payment row, không wallet credit, không trạng thái đối soát. Mọi provider transaction duy nhất phải hạ cánh vào ledger; entity đã terminal → ghi `manual_review`, không bao giờ biến mất. → [G-002]
- **Phòng ngừa**: Trong dispatch webhook, enumerate mọi nhánh terminal-state và bắt buộc `recordPayment` trước khi return; regression test "second transfer new provider_tx_id + same ref" assert ledger row tồn tại; đối chiếu dedup key với contract provider (SePay: transaction `id`, không phải nội dung CK).
- **Nguồn kiểm chứng**: review fix 2026-10-23 · game-item-broker (`sepay.service.ts` early-return `already_completed`/`order_not_pending`; spec 'records a manual_review ledger row')
  - **Occurrence 2 (2026-10-23 · game-item-broker r2 review)**: cùng mechanism ở nhánh concurrent — deposit claim update `affected=0` bị map thành `duplicated:true` cho provider_tx_id KHÁC nhau (không phải replay), và unmatched inbound (no-ref / ref-no-match) trả 200 trần. Fix: claim-loser + transition ConflictException + unmatched đều recordPayment manual_review; account_mismatch không ledger vì không phải tiền của mình.

### [P-058] Lệnh O(N) `KEYS` Quét Keyspace Redis Production & Lock TTL Cố Định Cho Batch Biến Thiên
- **Status**: active
- **Bài học**: Lệnh `KEYS` block Redis single-thread phục vụ trading; lock TTL cố định khiến distributed lock hết hạn giữa chừng khi batch kéo dài; xóa nhầm parent lock khi child processes còn running gây race condition. → [G-005]
- **Phòng ngừa**: Dùng `SCAN` cursor (`scanKeys`) thay cho `KEYS`; TTL lock cho batch phải scale động theo số lượng record `Math.max(60, count * avgTime)`; giữ parent lock tới khi mọi child hoàn tất.
- **Nguồn kiểm chứng**: PR #97 review 2026-10-29 · gsw-nx-monorepo (`admin-system-health.service.ts` comments #17, #24; thay keys bằng scanKeys + dynamic batch TTL)

### [P-059] Ngụy Tạo Số Liệu Đo Lường Telemetry Bằng Hệ Số Nhân (Fabricated Metrics)
- **Status**: active
- **Bài học**: Tuyệt đối không ngụy tạo số liệu quan sát (như gán `p95 = avg * 1.5`) khi thiếu mẫu đo — số liệu sai nguy hiểm hơn thiếu số liệu; frontend fetch dữ liệu thiếu Error State và Retry làm mất khả năng phục hồi của người dùng. → [G-001]
- **Phòng ngừa**: Trả `null` hoặc `unknown` khi mẫu đo không đủ điều kiện tính toán thống kê; mọi view/tab fetch dữ liệu bắt buộc implement Error State kèm Retry action.
- **Nguồn kiểm chứng**: PR #97 review 2026-10-29 · gsw-nx-monorepo (`admin-system-health.service.ts` comments #8, #10; portal UI health tab retry)

### [P-060] TypeORM `@BeforeInsert` Không Chạy Trên `createQueryBuilder().insert()` — PK Rỗng Nổ 23502 Trên DB Thật
- **Status**: draft
- **Bài học**: Entity lifecycle hooks (`@BeforeInsert`) chỉ chạy qua `save()`/`create()` — `createQueryBuilder().insert().values()` bypass hoàn toàn; PK text/uuid do hook sinh sẽ insert NULL → 23502 trên Postgres thật trong khi mock `execute()` vẫn xanh. Copy một orIgnore-insert pattern có sẵn = copy cả defect tiềm ẩn của nó. → [G-001, G-006]
- **Phòng ngừa**: Grep `.insert().values(` khi entity có hook-generated field (PK/audit) — supply tường minh trong `.values()`; insert path mới phải có ≥1 integration test trên DB thật; khi mirror pattern, verify từng field hook lẽ ra phải supply.
- **Nguồn kiểm chứng**: metadata.json (2026-10-30 · game-item-broker wallet-transactions integration)

### [P-061] Integration Specs Chung `PG_TEST_DSN` — `dropSchema`+`synchronize` Đua Nhau Dưới Parallel Jest Workers
- **Status**: draft
- **Bài học**: Khi nhiều `*.integration.spec.ts` đọc cùng một `PG_TEST_DSN` và mỗi spec `dropSchema:true` + `synchronize:true` trong `beforeAll`, jest chạy spec files trên parallel workers → các cửa sổ rebuild schema chồng lấn trên CÙNG database: `pg_type_typname_nsp_index` duplicate (enum type race) hoặc `relation "users" does not exist` (dropSchema của spec A xoá bảng giữa chừng spec B). Suite "xanh" trước đây chỉ là may mắn scheduling; thêm spec thứ ba đẩy xác suất va chạm lên ~luôn xảy ra. → [G-001]
- **Phòng ngừa**: Mỗi integration spec tự derive database riêng từ DSN (`<dsnDb>__<spec_name>`) và self-provision trong `beforeAll` (kết nối maintenance DB `postgres`, `CREATE DATABASE` nếu `pg_database` chưa có); giữ nguyên fail-fast guard chặn `game_item_broker`. Tuyệt đối không coi "suite integration share 1 scratch DB" là an toàn chỉ vì lần trước pass.
- **Nguồn kiểm chứng**: 2026-10-31 · game-item-broker orders-p1-hardening (`order-expiry.integration.spec.ts` + fix lan sang `payment-concurrency`/`wallet-transactions`; 39 suites/333 tests xanh ổn định sau khi mỗi spec có DB riêng)

### [P-062] Falsy-Fallback `|| 'dev-default'` Nuốt Input Credential — Account Lưu `pass123` Thay Password User Gõ
- **Status**: draft
- **Bài học**: Chuỗi component→context→apiClient mà mỗi tầng khai báo param optional + `|| 'literal-default'` sẽ lặng lặng thay input thật của user: `RegisterForm` gọi `register(name, email)` không truyền password, `apiClient` bù `password || 'pass123'` → account lưu `pass123` dù user gõ `Abcd@1234`; defect chỉ lộ ở login 401 "không hiểu tại sao". Falsy-fallback trên credential/user-input là data-loss mutation, không phải convenience. → [G-002]
- **Phòng ngừa**: Param credential bắt buộc ở type level (không `?:`); cấm `|| 'literal'` trên credential/user-input ở mọi tầng transport; prefill fixture phải bỏ trước khi user thật dùng; verify luồng auth bằng thao tác end-to-end (register rồi login lại bằng chính password đã gõ), không chỉ unit test.
- **Nguồn kiểm chứng**: 2026-11-02 · game-item-broker (DB `argon2.verify` chứng minh hash lưu = `pass123` khi user gõ `Abcd@1234`; fix xuyên RegisterForm→AuthContext→apiClient, login thật trả 200)

### [P-063] `flex-1` (basis 0) Trong "Scrollable" Strip — Item Co Bằng Nhau, Label Đè Nhau, Scroll Chết
- **Status**: draft
- **Bài học**: `flex:1 1 0%` trên item trong container `overflow-x-auto` chia đều width theo CONTAINER chứ không theo content — label `whitespace-nowrap` tràn đè item kề, và tổng basis = 0 nên không overflow → `overflow-x-auto` không bao giờ scroll. `shrink-0` không cứu được vì basis vẫn 0. Spec assert class-token (`overflow-x-auto`, `min-h-10`) xanh trong khi layout thật vẫn overlap — class presence ≠ layout contract. → [G-001, G-006]
- **Phòng ngừa**: Scrollable strip cần `flex:none`/`basis-auto` (hoặc đổi `grid-cols-N` khi số item cố định); verify touch-target/overflow bằng real-DOM measurement (`getBoundingClientRect`, `scrollWidth > clientWidth`) ở viewport thật, không chỉ class assertion.
- **Nguồn kiểm chứng**: 2026-11-02 · game-item-broker settings tabs (audit PNG overlap + Playwright 390px đo 3 tab 104px với text overflow trước fix → sau `grid-cols-3` + wrapped labels: 104×48px, overflow=false)

### [P-064] Mock Upstream Sai Wire Shape — Object Params Pass Test Nhưng Production -32700 Parse Error
- **Status**: draft
- **Bài học**: Adapter nodereal gửi `params` dạng JSON object trong khi NodeReal `nr_getAssetTransfers` bắt buộc positional array `[{...}]` — fake server trong unit test parse được object nên toàn bộ suite xanh, còn production luôn nhận `-32700 parse error` → `provider_status:degraded`. Mock acceptance ≠ upstream contract; chỉ live call chứng minh được wire shape đúng. → [G-006]
- **Phòng ngừa**: Mock phải parse đúng wire shape upstream thật yêu cầu (reject shape sai giống upstream); mọi external-integration adapter cần ít nhất 1 lần verify live với key thật trước khi coi là done — khi không có key, khai báo `live-verify: BLOCKED` thay vì claim done.
- **Nguồn kiểm chứng**: 2026-11-04 · check-wallet (adapter `LookupTransfers` fail degraded trên mọi call BSC; reproduce bằng curl object-params → -32700, array-params → 200; fix `[]any{params}` + sửa mock theo đúng shape; live verify `provider_status:ok`, 4000 rows thật)
