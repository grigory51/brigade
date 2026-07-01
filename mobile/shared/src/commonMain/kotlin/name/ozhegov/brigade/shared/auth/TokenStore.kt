package name.ozhegov.brigade.shared.auth

// TokenStore — платформенное защищённое хранилище токенов авторизации.
// Android — EncryptedSharedPreferences/Keystore, iOS — Keychain. На текущем шаге actual'ы
// упрощённые (in-memory / небезопасные заглушки) — отмечено в README и risks.
expect class TokenStore {
    fun accessToken(): String?
    fun refreshToken(): String?
    fun save(accessToken: String, refreshToken: String)
    fun clear()
}
