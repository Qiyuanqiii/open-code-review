# 리뷰 워크플로 플러그인 (Review Workflow Plugins)

패키지 설치, 실행 권한 및 런타임 어댑터 간의 관계는 [네이티브 플러그인 아키텍처 가이드](plugin-architecture.md)를 참조하십시오.

코드 리뷰에서 특정 실행 단계 순서와 완료 기준이 필요한 경우(예: 먼저 변경 사항을 검사한 후 후보 지적 사항을 별도 단계에서 재검증하고 최종 완료 보고서를 생성하는 경우) **리뷰 워크플로 플러그인**을 사용합니다. 패키지는 해당 단계를 선언하고 모델의 제출물을 검증하며, OCR 호스트는 diff, 규칙, 컨텍스트 도구 및 토큰 예산을 제공합니다.

## single-pass 예제 실행

Python 3이 `python`으로 사용 가능한 환경에서 예제를 설치하고 작업 트리의 변경 사항에 대해 미리보기를 실행합니다:

```sh
ocr plugin install ./examples/ocr-plugins/single-pass
ocr review --review-plugin single-pass --preview
```

모델 프로바이더가 설정되어 있고 리뷰할 변경 사항이 있는 상태에서 다음을 실행합니다:

```sh
ocr review --review-plugin single-pass --format json --output review.json
```

이 예제는 1개 단계로 구성됩니다. 모델에 지적 사항을 요청하고 OCR이 코멘트를 게시하기 전에 플러그인이 제출 내용을 검증합니다. `review.json`의 `review_plugin`에서 단계 완료 상태 및 보고서를 확인할 수 있습니다. 설치만으로는 패키지 코드가 실행되지 않으며, 미리보기(`--preview`)에서도 Python 프로세스나 모델을 호출하지 않습니다.

위 명령은 전역 설치된 패키지를 사용합니다. 리포지토리에 프로젝트 플러그인이 선언된 경우, 아래 프로젝트 워크플로를 사용하여 선언 및 승인해야 합니다. 전역 플러그인을 명시적으로 지정해도 프로젝트의 잠긴 세트를 우회할 수 없습니다.

## 프로젝트 선택 및 실행 권한

리포지토리에서 관리되는 워크플로의 경우 `ocr plugin add <source>`로 선언하고 `.opencodereview/plugins.json`과 `plugins.lock.json`을 커밋한 후 `ocr plugin sync --frozen`으로 동기화 설치합니다. `--review-plugin <id>`로 선택하기 전에 잠긴 패키지를 검사하고 `ocr plugin trust`를 실행하여 로컬 실행 권한을 부여하십시오. `ocr plugin preflight --json`을 실행하면 워크플로를 시작하거나 모델을 호출하지 않고도 설치 내용, 호환성 및 로컬 권한 상태를 검증할 수 있습니다. 자세한 내용은 [네이티브 OCR 플러그인 가이드](native-plugins.ko-KR.md#프로젝트-수준-플러그인-관리-서브커맨드)를 참조하십시오.

리뷰 워크플로 플러그인은 단계 계획, 프롬프트 지침, 완료 스키마, 검증 및 단계 간 상태를 자체적으로 유지합니다. OCR은 diff, 규칙, 모델/도구 실행, 세션, 토큰 계산, 코멘트 처리 및 출력을 담당합니다.

단일 실행에서 워크플로 선택:

```sh
ocr review --review-plugin PACKAGE_ID --preview
ocr review --review-plugin PACKAGE_ID --format json --output review.json
```

플러그인 설치나 활성화만으로는 워크플로가 실행되지 않습니다. `--preview`는 매니페스트만 검사하며 프로세스를 시작하지 않습니다. 실제 리뷰 실행 시에는 로컬 MCP 서버와 마찬가지로 현재 사용자의 로컬 권한으로 실행 파일이 실행됩니다. `--review-plugin`으로 명시적으로 선택한 경우, 전역에서 비활성화되어 있더라도 해당 플러그인의 규칙과 MCP 서버가 이번 실행에 함께 로드됩니다.

## 매니페스트 구성 (Manifest)

```json
{
  "schema_version": 1,
  "id": "example-review",
  "name": "Example Review",
  "version": "1.0.0",
  "review": {
    "protocol": "stdio-v1",
    "command": "${plugin_dir}/bin/${os}-${arch}/reviewer${exe}",
    "args": ["--policy", "${plugin_dir}/policy.md"],
    "stages": ["inspect", "verify"]
  }
}
```

`command`는 실행 파일 경로이며 셸 구문 분석을 거치지 않습니다. 명령어와 인자에서는 `${plugin_dir}`, `${os}`, `${arch}`, `${exe}`(Windows는 `.exe`, 기타 플랫폼은 빈 문자열)가 치환됩니다. PATH에 있는 `python` 등의 런타임도 지정할 수 있습니다. 번들된 Python 예제는 `python` 명령어를 기본으로 합니다. 시스템에 `python3`만 있는 경우 패키징 또는 설치 전에 매니페스트의 명령어를 `python3`으로 변경하십시오.

플러그인은 리뷰 워크플로, 규칙 및 MCP 도구를 자유롭게 결합할 수 있습니다. 단계 ID는 고유한 식별자이며 1개에서 64개의 순차적 실행 단계를 선언할 수 있습니다.

## stdio-v1 프로토콜 사양

OCR은 파일 그룹당 하나의 프로세스를 시작하며, 검토 대상 리포지토리를 작업 디렉터리로 사용합니다. 프로세스는 해당 그룹의 모든 단계에 걸쳐 활성 상태를 유지하며 단계 간 상태를 보존합니다. 요청과 응답은 한 줄의 UTF-8 JSON 객체(최대 4 MiB)입니다. 각 응답은 `"version": 1`을 포함해야 하며 진단 정보는 stderr로 출력합니다.

### 단계 시작 (`begin`)

각 단계 시작 전에 OCR이 전송합니다:

```json
{"version":1,"method":"begin","stage":"inspect","paths":["main.go"],"severities":["critical","high","medium","low"],"categories":["bug","security","performance","maintainability","test","style","documentation","other"]}
```

플러그인은 작업 지침과 초기 보고서를 반환합니다:

```json
{"version":1,"task":{"instructions":"선택된 코드를 검사하십시오.","input":"공개 API 반환 계약을 확인하십시오.","completion":{"description":"결과를 제출하십시오.","parameters":{"type":"object","properties":{"checked":{"type":"boolean"}},"required":["checked"]}}},"report":{"pending":["inspection"]}}
```

### 모델 제출 (`submit`)

모델이 `task_done`을 호출하면 OCR은 인자를 그대로 전달합니다:

```json
{"version":1,"method":"submit","stage":"inspect","arguments":{"checked":true}}
```

제출을 수락하는 응답에서는 명시적인 `accepted` 및 `complete` 불리언 값을 반환합니다:

```json
{"version":1,"accepted":true,"complete":true,"report":{"checked":true},"comments":[]}
```

모델의 제출물에 수정 가능한 오류가 있는 경우, 플러그인은 `accepted:false`, 비어 있지 않은 `feedback`, 최신 `report`를 반환하고 comments는 비워 둘 수 있습니다. OCR은 요청 허용량 내에서 모델에 피드백을 다시 전달합니다.

## 완료 상태 및 출력

선언된 모든 단계가 완료되어야 합니다. 사용자가 토큰 예산을 제공한 경우 사용되지 않은 잔여량은 후속 단계로 이월되어 공유됩니다.

JSON 출력에는 각 단계의 완료 상태와 보고서를 담은 `review_plugin` 배열이 포함됩니다. 실행 후 `ocr session show SESSION_ID --json`을 통해 세션에 저장된 보고서를 검사할 수 있습니다. 미완료 단계가 있는 경우 커버리지 매니페스트에 기록되며 0이 아닌 종료 코드가 반환됩니다.
