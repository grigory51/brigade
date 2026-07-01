package name.ozhegov.brigade.shared.net

import io.ktor.client.engine.HttpClientEngine
import io.ktor.client.engine.darwin.Darwin

// iOS-реализация платформенного движка Ktor: Darwin (NSURLSession).
actual fun httpClientEngine(): HttpClientEngine = Darwin.create()
