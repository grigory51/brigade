# Makefile мобильного клиента brigade (KMP + Compose Multiplatform).
# Цели — тонкие обёртки над Gradle-враппером проекта.

GRADLE := ./gradlew

.PHONY: assemble run-android link-ios clean

# Собрать shared-модуль (общий Kotlin).
assemble:
	$(GRADLE) :shared:assemble

# Установить и запустить Android-приложение на подключённом устройстве/эмуляторе.
run-android:
	$(GRADLE) :composeApp:installDebug

# Слинковать iOS-фреймворк KMP (arm64) — точка интеграции с iosApp.
link-ios:
	$(GRADLE) :shared:linkDebugFrameworkIosArm64

clean:
	$(GRADLE) clean
