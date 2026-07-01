# mobile — Kotlin Multiplatform + Compose Multiplatform (iOS + Android)

Каркас мобильного клиента brigade. На текущем шаге это только структура каталогов;
реализация (Gradle-проект, Ktor-клиент, экраны) выполняется отдельным шагом плана
(«Mobile-каркас»).

## Структура

- `shared/` — общий Kotlin-код.
  - `commonMain/` — Ktor-клиент (Connect-JSON поверх тех же эндпоинтов, что и web),
    модели, ANSI/VT-парсер терминала, обработка ACP/AG-UI, `expect`-объявления для
    auth/secure storage.
  - `androidMain/` — `actual`: engine OkHttp, Keystore.
  - `iosMain/` — `actual`: engine Darwin, Keychain.
- `composeApp/` — UI на Compose Multiplatform: экран логина (рабочий), CLI- и
  ACP-экраны (заглушки с навигацией).
- `iosApp/` — тонкая обёртка iOS (Xcode-проект, хостит Compose).
- `gradle/libs.versions.toml`, `settings.gradle.kts`, `build.gradle.kts` —
  конфигурация сборки (добавляются на шаге реализации).

## Контракт

Делит с web **только контракт** из `proto/`. Модели/клиент генерируются из той же proto
(буф-генерация Kotlin) либо описываются вручную как `@Serializable` data class под
Connect-JSON. Валидация контракта не дублируется между клиентами.

## Сборка (после реализации шага)

```
./gradlew :shared:assemble
./gradlew :shared:linkDebugFrameworkIosArm64
```
