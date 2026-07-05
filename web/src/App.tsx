import {
  BrowserRouter,
  Routes,
  Route,
  Navigate,
  useLocation,
} from "react-router-dom";
import { AuthProvider, useAuth } from "./features/auth/AuthContext";
import { LoginPage } from "./features/auth/LoginPage";
import { SessionLayout } from "./features/sessions/SessionLayout";
import { SessionEmpty } from "./features/sessions/SessionEmpty";
import { SessionPage } from "./features/sessions/SessionPage";
import { ArchivePage, ArchiveSessionPage } from "./features/sessions/ArchivePage";
import { SettingsPage } from "./features/settings/SettingsPage";
import { RouteSpinner } from "./lib/RouteSpinner";

// Гейт для защищённых маршрутов. Пока идёт первичная проверка сессии (ready ===
// false), показываем нейтральный экран загрузки, иначе уже залогиненного
// пользователя выбросило бы на /login из-за гонки с асинхронным Me.
function RequireAuth({ children }: { children: React.ReactNode }) {
  const { user, ready } = useAuth();
  const location = useLocation();

  if (!ready) {
    return <RouteSpinner />;
  }
  if (!user) {
    return <Navigate to="/login" replace state={{ from: location.pathname }} />;
  }
  return <>{children}</>;
}

// Если пользователь уже вошёл, /login перенаправляет на список сессий.
function RedirectIfAuthed({ children }: { children: React.ReactNode }) {
  const { user, ready } = useAuth();
  if (!ready) {
    return <RouteSpinner />;
  }
  if (user) return <Navigate to="/sessions" replace />;
  return <>{children}</>;
}

export function App() {
  return (
    <AuthProvider>
      <BrowserRouter>
        <Routes>
          <Route
            path="/login"
            element={
              <RedirectIfAuthed>
                <LoginPage />
              </RedirectIfAuthed>
            }
          />
          {/* Общая оболочка с sidebar — layout-роут: список сессий слева постоянен,
              контент справа меняется через <Outlet/>. /sessions (index) — пустое
              состояние, /s/:sessionId — конкретная сессия. */}
          <Route
            element={
              <RequireAuth>
                <SessionLayout />
              </RequireAuth>
            }
          >
            <Route path="/sessions" element={<SessionEmpty />} />
            <Route path="/s/:sessionId" element={<SessionPage />} />
            <Route path="/archive" element={<ArchivePage />} />
            <Route path="/archive/:sessionId" element={<ArchiveSessionPage />} />
            <Route path="/settings" element={<SettingsPage />} />
          </Route>
          <Route path="*" element={<Navigate to="/sessions" replace />} />
        </Routes>
      </BrowserRouter>
    </AuthProvider>
  );
}
