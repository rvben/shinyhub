import jwt
import pytest

from shinyhub_identity import IdentityError, current_user, verify_token
from shinyhub_identity.testing import DEFAULT_KEY, DEFAULT_SLUG, identity_env, mint_token


def test_entitlements_are_separate_from_groups_and_old_tokens_work():
    with identity_env():
        user = verify_token(mint_token(groups=["finance"], entitlements=["power_user"]))
        assert user.entitlements == ("power_user",)
        assert user.groups == ("finance",)
        assert user.role == "viewer"
        assert verify_token(mint_token()).entitlements == ()


@pytest.mark.parametrize("value", [None, "power_user", {"role": "power_user"}, [1], [""]])
def test_malformed_entitlement_claim_is_rejected(value):
    claims = jwt.decode(mint_token(), DEFAULT_KEY, algorithms=["HS256"], audience=DEFAULT_SLUG)
    claims["entitlements"] = value
    token = jwt.encode(claims, DEFAULT_KEY, algorithm="HS256")
    with pytest.raises(IdentityError) as error:
        verify_token(token, key=DEFAULT_KEY, slug=DEFAULT_SLUG)
    assert error.value.reason == "malformed"


def test_dev_entitlements(monkeypatch):
    monkeypatch.delenv("SHINYHUB_IDENTITY_KEY", raising=False)
    monkeypatch.setenv("SHINYHUB_IDENTITY_DEV_USER", "analyst")
    monkeypatch.setenv("SHINYHUB_IDENTITY_DEV_ENTITLEMENTS", "power_user, report_export")
    assert current_user({}).entitlements == ("power_user", "report_export")
