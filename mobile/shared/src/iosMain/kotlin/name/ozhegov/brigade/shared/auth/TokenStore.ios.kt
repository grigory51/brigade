package name.ozhegov.brigade.shared.auth

// iOS-реализация TokenStore. На текущем шаге — небезопасная in-memory заглушка
// (токены живут только в памяти процесса и теряются при перезапуске), как и
// отмечено в README. Защищённое хранилище на Keychain добавляется отдельным шагом.
actual class TokenStore {
    private var access: String? = null
    private var refresh: String? = null

    actual fun accessToken(): String? = access

    actual fun refreshToken(): String? = refresh

    actual fun save(accessToken: String, refreshToken: String) {
        access = accessToken
        refresh = refreshToken
    }

    actual fun clear() {
        access = null
        refresh = null
    }
}
